package serve

import (
	"github.com/Dicklesworthstone/ntm/internal/backpressure"
)

// BackpressureInputs returns the server-side overload counters that are cheap
// to inspect without touching tmux or forcing a dashboard refresh.
func (s *Server) BackpressureInputs() []backpressure.SurfaceInput {
	if s == nil {
		return []backpressure.SurfaceInput{missingServeInput(backpressure.SurfaceREST, "server is nil")}
	}
	inputs := make([]backpressure.SurfaceInput, 0, 2)
	if s.wsHub != nil {
		inputs = append(inputs, s.wsHub.BackpressureInput())
	} else {
		inputs = append(inputs, missingServeInput(backpressure.SurfaceWebSocket, "WebSocket hub is unavailable."))
	}
	inputs = append(inputs, missingServeInput(backpressure.SurfaceREST, "REST handler queue metrics are not wired yet."))
	return inputs
}

// BackpressureInput reports WebSocket broadcast queue and drop state.
func (h *WSHub) BackpressureInput() backpressure.SurfaceInput {
	if h == nil {
		return missingServeInput(backpressure.SurfaceWebSocket, "WebSocket hub is unavailable.")
	}
	return backpressure.SurfaceInput{
		Surface:       backpressure.SurfaceWebSocket,
		QueueDepth:    len(h.broadcast),
		QueueCapacity: cap(h.broadcast),
		DroppedCount:  h.DroppedEvents(),
		SourceLoaded:  true,
	}
}

// DroppedEvents returns events dropped by the hub or by lagging client buffers.
func (h *WSHub) DroppedEvents() int64 {
	if h == nil {
		return 0
	}
	return h.dropped.Load()
}

// BackpressureInput reports SSE attention stream queue and drop state.
func (p *preparedAttentionStream) BackpressureInput(session string) backpressure.SurfaceInput {
	if p == nil {
		return backpressure.SurfaceInput{
			Surface:        backpressure.SurfaceSSE,
			Session:        session,
			SourceLoaded:   false,
			MissingWarning: "SSE attention stream is unavailable.",
		}
	}
	return backpressure.SurfaceInput{
		Surface:       backpressure.SurfaceSSE,
		Session:       session,
		QueueDepth:    len(p.eventCh),
		QueueCapacity: cap(p.eventCh),
		DroppedCount:  int64(p.droppedCount.Load()),
		SourceLoaded:  true,
	}
}

func missingServeInput(surface backpressure.Surface, hint string) backpressure.SurfaceInput {
	return backpressure.SurfaceInput{
		Surface:        surface,
		SourceLoaded:   false,
		MissingWarning: hint,
	}
}
