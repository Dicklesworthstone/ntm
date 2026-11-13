// kernel_catalog.go converges internal/kernel with --robot-capabilities and
// the served /api/v1/* router so `ntm kernel list` and Web UI Ctrl+K
// (`GET /api/v1/kernel/commands`) expose one authoritative command catalog
// without duplicating hand-maintained descriptors (bd-rc-track-e-uplift-xkt68.1).
package serve

import (
	"fmt"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/go-chi/chi/v5"

	"github.com/Dicklesworthstone/ntm/internal/kernel"
	"github.com/Dicklesworthstone/ntm/internal/robot"
)

var (
	kernelRobotSyncOnce  sync.Once
	kernelRouterSyncOnce sync.Once
	kernelRouterSynced   atomic.Bool
)

// EnsureKernelCatalogRegistered idempotently populates the default kernel
// registry from --robot-capabilities and the served /api/v1/* router. Explicit
// commands registered via kernel.MustRegister take precedence.
func EnsureKernelCatalogRegistered() {
	syncKernelRobotCapabilities()
	if !kernelRouterSynced.Load() {
		srv := NewHermeticServer("dev")
		srv.Stop()
	}
}

func syncKernelCatalogFromRouter(router chi.Router) {
	syncKernelRobotCapabilities()
	if router == nil {
		return
	}
	kernelRouterSyncOnce.Do(func() {
		defer kernelRouterSynced.Store(true)
		routes, err := WalkServedRoutes(router)
		if err != nil {
			return
		}
		for _, route := range routes {
			if !strings.HasPrefix(route.Pattern, "/api/v1/") {
				continue
			}
			// Skip streaming upgrade endpoints from Ctrl+K one-shot REST invocation.
			if route.Pattern == "/api/v1/ws" || route.Pattern == "/api/v1/attention/stream" {
				continue
			}
			cmd := kernelCommandFromServedRoute(route)
			kernel.RegisterIfAbsent(cmd)
		}
	})
}

func syncKernelRobotCapabilities() {
	kernelRobotSyncOnce.Do(func() {
		caps, err := robot.GetCapabilities()
		if err != nil || caps == nil {
			return
		}
		for _, info := range caps.Commands {
			name := strings.TrimSpace(info.Name)
			if name == "" {
				continue
			}
			kernelName := "robot." + strings.ReplaceAll(name, "-", "_")
			desc := strings.TrimSpace(info.Summary)
			if desc == "" {
				desc = strings.TrimSpace(info.Description)
			}
			if desc == "" {
				desc = fmt.Sprintf("Run %s", info.Flag)
			}
			cat := strings.TrimSpace(info.Category)
			if cat == "" {
				cat = "robot"
			}

			var restBinding *kernel.RESTBinding
			for _, tr := range info.Transports {
				if strings.EqualFold(tr.Type, "rest") && strings.TrimSpace(tr.Endpoint) != "" {
					method := strings.ToUpper(strings.TrimSpace(tr.Method))
					if method == "" {
						method = "GET"
					}
					ep := strings.TrimSpace(tr.Endpoint)
					// Skip streaming endpoints in one-shot REST bindings.
					if ep == "/api/v1/ws" || ep == "/api/v1/attention/stream" {
						continue
					}
					restBinding = &kernel.RESTBinding{
						Method: method,
						Path:   ep,
					}
					break
				}
			}

			examples := make([]kernel.Example, 0, len(info.Examples))
			for idx, ex := range info.Examples {
				trimmed := strings.TrimSpace(ex)
				if trimmed == "" {
					continue
				}
				examples = append(examples, kernel.Example{
					Name:        fmt.Sprintf("example-%d", idx+1),
					Description: desc,
					Command:     trimmed,
				})
			}
			if len(examples) == 0 {
				cmdStr := "ntm " + strings.TrimSpace(info.Flag)
				examples = append(examples, kernel.Example{
					Name:        "default",
					Description: desc,
					Command:     strings.TrimSpace(cmdStr),
				})
			}

			var outRef *kernel.SchemaRef
			if st := strings.TrimSpace(info.SchemaType); st != "" {
				outRef = &kernel.SchemaRef{
					Name: st,
					Ref:  "robot." + st,
				}
			}

			safety := kernel.SafetySafe
			idempotent := true
			if restBinding != nil && restBinding.Method != "GET" {
				idempotent = restBinding.Method == "PUT" || restBinding.Method == "DELETE"
				if restBinding.Method == "DELETE" {
					safety = kernel.SafetyDanger
				} else {
					safety = kernel.SafetyCaution
				}
			} else if cat == "control" || cat == "spawn" {
				safety = kernel.SafetyCaution
				idempotent = false
			}

			kernel.RegisterIfAbsent(kernel.Command{
				Name:        kernelName,
				Description: desc,
				Category:    cat,
				Output:      outRef,
				REST:        restBinding,
				Examples:    examples,
				SafetyLevel: safety,
				Idempotent:  idempotent,
			})
		}
	})
}

func kernelCommandFromServedRoute(route RouterRoute) kernel.Command {
	trimmed := strings.TrimPrefix(route.Pattern, "/api/v1/")
	parts := strings.Split(trimmed, "/")
	category := "rest"
	if len(parts) > 0 && parts[0] != "" && !strings.HasPrefix(parts[0], "{") {
		category = parts[0]
	}

	slugParts := make([]string, 0, len(parts)+1)
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if strings.HasPrefix(p, "{") && strings.HasSuffix(p, "}") {
			p = "by_" + strings.Trim(p, "{}")
		}
		p = strings.NewReplacer("-", "_", ".", "_").Replace(p)
		slugParts = append(slugParts, p)
	}
	methodLower := strings.ToLower(route.Method)
	name := "rest." + strings.Join(slugParts, ".") + "." + methodLower
	summary := routeSummary(route)
	if strings.TrimSpace(summary) == "" {
		summary = route.Method + " " + route.Pattern
	}

	safety := kernel.SafetySafe
	idempotent := route.Method == "GET" || route.Method == "PUT" || route.Method == "DELETE"
	if route.Method == "DELETE" {
		safety = kernel.SafetyDanger
	} else if route.Method != "GET" {
		safety = kernel.SafetyCaution
	}

	return kernel.Command{
		Name:        name,
		Description: summary,
		Category:    category,
		REST: &kernel.RESTBinding{
			Method: route.Method,
			Path:   route.Pattern,
		},
		Examples: []kernel.Example{
			{
				Name:        "rest",
				Description: summary,
				Command:     fmt.Sprintf("curl -X %s %s%s", route.Method, DefaultOpenAPIServerURL, route.Pattern),
			},
		},
		SafetyLevel: safety,
		Idempotent:  idempotent,
	}
}
