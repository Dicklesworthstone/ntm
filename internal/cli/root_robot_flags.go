package cli

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/Dicklesworthstone/ntm/internal/agent"
	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/plugins"
	"github.com/Dicklesworthstone/ntm/internal/util"
)

// Robot output flags for AI agent integration
var (
	robotHelp                  bool
	robotStatus                bool
	robotVersion               bool
	robotCapabilities          bool
	robotCapabilitiesCommand   string
	robotCapabilitiesCategory  string
	robotCapabilitiesSearch    string
	robotCapabilitiesCompact   bool
	robotDocs                  string // --robot-docs topic
	robotPlan                  bool
	robotSnapshot              bool   // unified state query
	robotSince                 string // ISO8601 timestamp for delta snapshot
	robotEvents                bool   // raw replay/feed surface
	robotEventsSinceCursor     int64  // cursor for --robot-events
	robotEventsLimit           int    // max events for --robot-events
	robotEventsIncident        string // durable incident id for bounded replay
	robotEventsAsOf            string // RFC3339 timestamp for bounded as-of reconstruction
	robotEventsWindowBefore    string // duration before incident start for replay context
	robotEventsWindowAfter     string // duration after incident end for replay context
	robotEventsCategory        string // category filter for --robot-events
	robotEventsSession         string // session filter for --robot-events
	robotEventsActionability   string // actionability filter for --robot-events
	robotProfile               string // filter profile for attention-feed commands (br-91gti)
	robotAttention             bool   // one obvious tending primitive (br-t540i)
	robotDigest                bool   // non-blocking digest of attention state (br-rkh17)
	robotAttentionSinceCursor  int64  // cursor for --robot-attention
	robotAttentionSession      string // session filter for --robot-attention
	robotAttentionTimeout      string // timeout for --robot-attention
	robotAttentionPoll         string // poll interval for --robot-attention
	robotAttentionCondition    string // attention condition to wait for
	robotTail                  string // session name for tail
	robotTailFresh             bool   // force direct live capture per pane (ntm-mxcp)
	robotWatchBead             string // session name for bead mention watch
	robotWatchBeadID           string // bead ID for watch command
	robotErrors                string // session name for errors
	robotLines                 int    // number of lines to capture
	robotPanes                 string // comma-separated pane filter
	robotGraph                 bool   // bv insights passthrough
	robotBeadLimit             int    // limit for ready/in-progress beads in snapshot
	robotLimit                 int    // pagination limit for robot list outputs
	robotOffset                int    // pagination offset for robot list outputs
	robotDashboard             bool   // dashboard summary output
	robotContext               string // session name for context usage
	robotEnsemble              string // session name for ensemble state
	robotEnsembleSpawn         string // session name for ensemble spawn
	robotEnsembleModesList     bool   // list reasoning modes via robot API
	robotEnsemblePresetsList   bool   // list ensemble presets via robot API
	robotEnsembleTier          string // tier filter for robot-ensemble-modes
	robotEnsemblePreset        string // preset name for ensemble spawn
	robotEnsembleModes         string // mode IDs/codes for ensemble spawn
	robotEnsembleQuestion      string // question for ensemble spawn
	robotEnsembleAgents        string // agent mix for ensemble spawn
	robotEnsembleAssignment    string // assignment strategy for ensemble spawn
	robotEnsembleAllowAdvanced bool   // allow advanced modes
	robotEnsembleBudgetTotal   int    // total token budget override
	robotEnsembleBudgetPerMode int    // per-mode token budget override
	robotEnsembleNoCache       bool   // disable context cache
	robotEnsembleProject       string // project directory override
	robotEnsembleSuggest       string // question for ensemble suggestion
	robotEnsembleSuggestIDOnly bool   // output only preset name
	robotEnsembleStop          string // session name to stop
	robotEnsembleStopForce     bool   // force kill without graceful shutdown
	robotEnsembleStopNoCollect bool   // skip partial output collection

	// Robot-overlay flags for agent-initiated human handoff (br-a6cmp)
	robotOverlay        bool   // open overlay for human handoff
	robotOverlaySession string // session for overlay
	robotOverlayCursor  int64  // attention cursor for pre-focus
	robotOverlayNoWait  bool   // return immediately without blocking

	// Robot-send flags
	robotSend             string // session name for send
	robotSendMsg          string // message to send
	robotSendMsgFile      string // file containing message to send
	robotSendEnter        bool   // send Enter after pasting message
	robotSendClearInput   bool   // clear residual composer text before typing (ntm-5p0b)
	robotSendVerifyRender bool   // capture bounded before/after evidence for --robot-send
	robotSendAll          bool   // send to all panes
	robotSendType         string // filter by agent type (e.g., "claude")
	robotSendExclude      string // comma-separated panes to exclude
	robotSendDelay        int    // delay between sends in ms
	robotOpID             string // durable idempotent operation ID for --robot-send (#245) and --robot-interrupt
	robotSendReceipt      string // operation ID for --robot-send-receipt (#245): send or interrupt receipt
	robotActionStatus     string // request/operation ID for --robot-action-status
	robotWaitRequest      string // request/operation ID for --robot-wait-request
	robotCancelRequest    string // request/operation ID for --robot-cancel-request
	robotRequestID        string // explicit --request-id selector for action-status/wait-request/cancel-request
	robotSendWithMemory   bool   // inject CM memory rules into the outgoing message (bd-3j6hm)
	robotSendWithCASS     bool   // inject CASS session context into the outgoing message (bd-ws2-wire-or-delete-ykmcz.11)
	robotSendNoCASS       bool   // force-disable CASS injection, overriding [cass.context] enabled=true

	// Robot-assign flags for work distribution
	robotAssign         string // session name for work assignment
	robotAssignBeads    string // comma-separated bead IDs to assign
	robotAssignStrategy string // assignment strategy: simple, balanced, speed, quality, dependency
	robotAssignRelax    bool   // Lower only the score floor when strict planning places no work.

	// Robot-bulk-assign flags for batch work distribution
	robotBulkAssign         string        // session name for bulk assignment
	robotBulkAssignFromBV   bool          // use bv triage for bead selection
	robotBulkAssignAlloc    string        // explicit allocation JSON
	robotBulkAssignStrategy string        // assignment strategy: impact, ready, stale, balanced
	robotBulkAssignSkip     string        // comma-separated panes to skip
	robotBulkAssignTemplate string        // prompt template file path
	robotBulkAssignParallel bool          // execute independent bulk assignments concurrently
	robotBulkAssignStagger  time.Duration // delay between sequential bulk assignments
	robotReserveFiles       bool          // reserve each bulk assignment's file scope (default true)
	robotRequireReservation bool          // fail closed unless spawn assignment reservations succeed
	robotReservationPaths   string        // comma-separated reservation path globs

	// Robot-health flag
	robotHealth             string // session health or project health (empty = project)
	robotHealthOAuth        string // session OAuth/rate-limit status
	robotHealthRestartStuck string // session name for auto-restart-stuck
	robotStuckThreshold     string // duration before considering stuck (e.g. 5m)

	// Robot-logs flags
	robotLogs      string // session name for logs
	robotLogsPanes string // comma-separated pane indices
	robotLogsLimit int    // max lines per pane

	// Robot-diagnose flags
	robotDiagnose      string // session name for comprehensive diagnosis
	robotDiagnoseFix   bool   // attempt auto-fix
	robotDiagnoseBrief bool   // minimal output mode
	robotDiagnosePane  int    // specific pane to diagnose (-1 = all)

	// Robot-recipes flag
	robotRecipes bool // list available recipes as JSON

	// Robot-schema flag
	robotSchema string // schema type to generate

	// Robot-mail flag
	robotMail bool // Agent Mail state output

	// Robot-tools flag for tool inventory
	robotTools bool // tool inventory and health output

	// Robot-acfs/setup flags
	robotACFSStatus bool // ACFS setup status
	robotSetup      bool // alias for ACFS setup status

	// Robot-ack flags for send confirmation tracking
	robotAck        string // session name for ack
	robotAckTimeout string // timeout (e.g., "30s", "5000ms")
	robotAckPoll    int    // poll interval in milliseconds
	robotAckTrack   bool   // combined send+ack mode

	// Robot-spawn flags for structured session creation
	robotSpawn           string             // session name for spawn
	robotSpawnCC         string             // Claude agents: count[:model[:effort]]
	robotSpawnCod        string             // Codex agents: count[:model[:effort]]
	robotSpawnGmi        string             // Gemini agents: count[:model]
	robotSpawnAgy        string             // Antigravity agents: count (model pinned)
	robotSpawnGrok       string             // Grok Build agents: count[:model[:effort]]
	robotSpawnOmp        string             // Oh My Pi (omp) agents: count[:model[:effort]]
	robotSpawnOc         string             // OpenCode (oc) agents: count[:model]
	robotSpawnOllama     string             // Ollama local agents: count[:model]
	robotSpawnAider      string             // Aider agents: count[:model]
	robotSpawnCursor     string             // Cursor Agent CLI agents: count[:model]
	robotSpawnWindsurf   string             // Windsurf agents: count[:model] (IDE_ONLY_HARNESS unless [agents] windsurf is overridden)
	robotSpawnPlugins    map[string]*string // Canonical registered plugin -> dynamic flag value
	robotSpawnPreset     string             // recipe/preset name
	robotSpawnNoUser     bool               // don't create user pane
	robotSpawnWait       bool               // wait for agents to be ready
	robotSpawnTimeout    string             // timeout for ready detection (e.g., "30s", "1m")
	robotSpawnSafety     bool               // fail if session already exists
	robotSpawnDir        string             // working directory override
	robotSpawnAssignWork bool               // enable orchestrator work assignment mode
	robotSpawnStrategy   string             // assignment strategy: top-n, diverse, dependency-aware, skill-matched
	robotSpawnNames      string             // custom agent names (comma-separated)
	robotSpawnLabel      string             // goal label for multi-session support
	// Initial prompt + thundering-herd pacing for --robot-spawn
	robotSpawnPrompt       string // initial prompt delivered to every agent after readiness
	robotSpawnPromptFile   string // file (or '-' for stdin) holding the initial prompt
	robotSpawnStaggerMode  string // none, fixed, smart ("" = [spawn] stagger_mode)
	robotSpawnStaggerDelay string // fixed-mode interval ("" = [spawn] stagger_delay)

	// Robot-agent-names flag for querying agent name mappings
	robotAgentNames string // session name for --robot-agent-names

	// Robot-controller-spawn flags for launching controller agents
	robotControllerSpawn     string // session name for controller spawn
	robotControllerAgentType string // agent type: cc, cod, gmi, agy
	robotControllerPrompt    string // custom prompt file
	robotControllerNoPrompt  bool   // skip sending initial prompt

	// Robot-interrupt flags for priority course correction
	robotInterrupt        string // session name for interrupt
	robotInterruptMsg     string // message to send after interrupt
	robotInterruptAll     bool   // include all panes (including user)
	robotInterruptForce   bool   // send Ctrl+C even if agent appears idle
	robotInterruptNoWait  bool   // don't wait for ready state
	robotInterruptTimeout string // timeout for ready state (e.g., "10s", "5000ms")

	// Robot-terse flag for ultra-compact output
	robotTerse bool // single-line encoded state

	// Robot-format flag for output serialization format
	robotFormat    string // json, toon, or auto
	robotVerbosity string // terse, default, or debug

	// Robot-markdown flags for token-efficient markdown output
	robotMarkdown          bool   // markdown output mode
	robotMarkdownCompact   bool   // ultra-compact markdown
	robotMarkdownSession   string // filter to specific session
	robotMarkdownSections  string // comma-separated sections to include
	robotMarkdownMaxBeads  int    // max beads per category
	robotMarkdownMaxAlerts int    // max alerts to show

	// Robot-save flags for session state persistence
	robotSave       string // session name to save
	robotSaveOutput string // custom output file path

	// Robot-restore flags for session state restoration
	robotRestore    string // saved state name to restore
	robotRestoreDry bool   // dry-run mode
	robotDryRun     bool   // shared dry-run mode for robot actions (--dry-run)

	// Robot-cass flags for CASS integration
	robotCassStatus   bool   // CASS health check
	robotCassSearch   string // search query
	robotCassInsights bool   // aggregated insights
	robotCassContext  string // context query
	cassAgent         string // filter by agent
	cassWorkspace     string // filter by workspace
	cassSince         string // filter by time
	cassLimit         int    // max results

	// Robot-jfp flags for JeffreysPrompts integration
	robotJFPStatus     bool   // JFP health check
	robotJFPList       bool   // list all prompts
	robotJFPSearch     string // search query
	robotJFPShow       string // prompt ID to show
	robotJFPSuggest    string // task for suggestions
	robotJFPExport     string // prompt IDs to export
	robotJFPUpdate     bool   // update JFP registry cache
	robotJFPCategories bool   // list categories
	robotJFPTags       bool   // list tags
	robotJFPBundles    bool   // list bundles
	jfpCategory        string // filter by category
	jfpTag             string // filter by tag
	jfpFormat          string // export format

	// Robot-ms flags for Meta Skill integration
	robotMSSearch string // search query
	robotMSShow   string // skill ID to show

	// Robot-xf flags for XF (X Find) archive search integration
	robotXFSearch       string // search query
	robotXFStatus       bool   // health check
	robotDefaultPrompts bool   // show per-agent-type default prompts
	robotProfileList    bool   // list session profiles (bd-29kr)
	robotProfileShow    string // show session profile by name (bd-29kr)
	xfLimit             int    // max search results
	xfMode              string // search mode: lexical, semantic, hybrid, two-tier
	xfSort              string // sort: relevance, date

	// Robot-tokens flags for token usage analysis
	robotTokens        bool   // token usage output
	robotTokensDays    int    // number of days to analyze
	robotTokensSince   string // ISO8601 timestamp to analyze since
	robotTokensGroupBy string // grouping: agent, model, day, week, month
	robotTokensSession string // filter to session
	robotTokensAgent   string // filter to agent type

	// Robot-history flags for command history tracking
	robotHistory      string // session name for history query
	robotHistoryPane  string // filter by pane ID
	robotHistoryType  string // filter by agent type
	robotHistoryLast  int    // last N entries
	robotHistorySince string // time-based filter
	robotHistoryStats bool   // show statistics instead of entries

	// Robot-causality flags for unified causality timeline queries
	robotCausality        string // session name for causality query
	robotCausalityProject string // project path for agent mail + pipeline state reads
	robotCausalityAgent   string // Agent Mail agent name for inbox fetch
	robotCausalityBead    string // bead/thread filter
	robotCausalityPane    string // pane filter
	robotCausalityType    string // normalized event type filter
	robotCausalityChain   string // causal chain/correlation filter
	robotCausalitySince   string // lower time bound
	robotCausalityUntil   string // upper time bound
	robotCausalityLimit   int    // max output events

	// Robot-activity flags for agent activity detection
	robotActivity           string // session name for activity query
	robotActivityType       string // filter by agent type (claude, codex, antigravity, grok, gemini)
	robotProductivity       string // session name for productivity evidence query
	robotProductivityWindow string // observation window for productivity and convergence
	robotConvergedStreak    int    // consecutive converged observations required by robot wait

	// Robot-wait flags for waiting on agent states
	robotWait           string // session name for wait
	robotWaitUntil      string // wait condition: idle, complete, generating, healthy
	robotWaitTimeout    string // timeout (e.g., "30s", "5m")
	robotWaitPoll       string // poll interval (e.g., "2s", "500ms")
	robotWaitPanes      string // comma-separated pane indices
	robotWaitType       string // filter by agent type
	robotWaitAny        bool   // wait for ANY agent (vs ALL)
	robotWaitOnError    bool   // exit immediately on error state
	robotWaitTransition bool   // require state transition before returning
	robotWaitID         string // optional durable cancellation handle
	robotWaitCancel     string // handle to cancel

	// Robot-route flags for routing recommendations
	robotRoute          string // session name for route
	robotRouteStrategy  string // routing strategy (least-loaded, first-available, round-robin, etc.)
	robotRouteLastAgent string // pane ID of the previously routed agent (sticky/round-robin anchor)
	robotRouteType      string // filter by agent type (claude, codex, gemini)
	robotRouteExclude   string // comma-separated pane indices to exclude

	// Robot-sequence flags for durable per-pane prompt progress.
	robotSequence       string // durable per-pane prompt sequence name
	robotSequenceAction string // create, next, or advance
	robotSequencePane   string // stable tmux pane id or canonical pane address
	robotSequenceSteps  string // JSON prompt array for create

	// Robot-pipeline flags for workflow execution
	robotPipelineRun       string // workflow file to run
	robotPipelineStatus    string // run ID to check status
	robotPipelineList      bool   // list all pipelines
	robotPipelineCancel    string // run ID to cancel
	robotPipelineSession   string // session name for pipeline execution
	robotPipelineVars      string // JSON variables for pipeline
	robotPipelineDryRun    bool   // validate without executing
	robotPipelineBG        bool   // run in background
	robotPipelineStartFrom string // bd-9296v: top-level step ID to start from (mirrors `ntm pipeline run --start-from`)
	robotPipelineFromState string // bd-9296v: prior run ID whose persisted outputs feed steps skipped by --start-from
	robotSharedSession     string // shared --session flag: canonical form for the deprecated *-session prefixed flags (ntm#214)

	// TUI Parity robot flags - expose TUI dashboard functionality to AI agents
	robotFiles                 string // session name for file changes query
	robotFilesWindow           string // time window: 5m, 15m, 1h, all (default: 15m)
	robotFilesLimit            int    // max changes to return
	robotInspectWork           string // bead id for projection-backed work inspection
	robotInspectCoord          string // agent mail identity for projection-backed coordination inspection
	robotInspectQuota          string // provider/account for projection-backed quota inspection
	robotInspectIncident       string // incident id for store-backed incident inspection
	robotIncidentResolve       string // incident id to mark resolved (write verb for the incident lifecycle)
	robotExitCLI               string // session for graceful agent-CLI exit without destroying the pane
	robotKillAgent             string // session for hard agent-process kill preserving the pane shell
	robotLifecycleRelaunch     bool   // relaunch the agent CLI after --robot-exit-cli/--robot-kill-agent
	robotExitCLIRespawnShell   bool   // replace exited direct-exec root agent with a fresh shell in the same cwd
	robotExitCLIAllowPaneClose bool   // permit direct-exec root agent exit to close its pane cleanly
	robotKillPane              string // session for per-pane removal (session and siblings survive)
	robotDialogs               string // session for per-pane dialog classification
	robotAnswerDialog          string // session for policy-gated dialog answering
	robotDialogChoice          string // choice for --robot-answer-dialog
	robotIncidentNote          string // optional resolution note recorded with --robot-incident-resolve
	robotInspectSession        string // session name for projection-backed session inspection
	robotInspectAgent          string // runtime agent id for projection-backed agent inspection
	robotInspectPane           string // session name for pane inspection
	robotInspectIndex          int    // pane index to inspect
	robotInspectLines          int    // lines to capture for inspection
	robotInspectCode           bool   // parse code blocks in output
	robotMetrics               string // session name for metrics
	robotMetricsPeriod         string // period: 1h, 24h, 7d, all
	robotDiskAttribution       bool   // include per-pane build-dir disk attribution in metrics
	robotReplay                string // session name for replay
	robotReplayID              string // history entry ID to replay
	robotReplayDryRun          bool   // just show what would be replayed
	robotPaletteInfo           bool   // query palette information
	robotPaletteSession        string // filter to session
	robotPaletteCategory       string // filter by category
	robotPaletteSearch         string // search query
	robotDismissAlert          string // alert ID to dismiss
	robotDismissSession        string // session scope for alert dismissal
	robotDismissAll            bool   // dismiss all matching alerts

	// Robot-diff flags for comparing agent activity
	robotDiff      string // session name for diff
	robotDiffSince string // duration like "10m" or RFC3339 timestamp

	// Robot-alerts flags for alert listing
	robotAlerts         bool   // list alerts
	robotAlertsSeverity string // filter by severity
	robotAlertsType     string // filter by alert type
	robotAlertsSession  string // filter by session

	// Robot-beads-list flags for bead listing
	robotBeadsList     bool   // list beads
	robotBeadsStatus   string // filter by status: open, in_progress, closed, blocked
	robotBeadsPriority string // filter by priority: 0-4 or P0-P4
	robotBeadsAssignee string // filter by assignee
	robotBeadsType     string // filter by type: task, bug, feature, epic, chore
	robotBeadsLimit    int    // max beads to return

	// Robot-bead flags for programmatic bead management
	robotBeadClaim  string // bead ID to claim
	robotBeadCreate bool   // create a new bead
	robotBeadShow   string // bead ID to show details
	robotBeadClose  string // bead ID to close
	beadTitle       string // title for new bead
	beadType        string // type: task, bug, feature, epic, chore
	beadPriority    int    // priority: 0-4
	beadDescription string // description for new bead
	beadLabels      string // comma-separated labels
	beadDependsOn   string // comma-separated dependency IDs
	beadAssignee    string // assignee for claim
	beadCloseReason string // reason for closing

	// Robot-summary flags for session summary
	robotSummary      string // session name for summary
	robotSummarySince string // duration like "30m" or RFC3339 timestamp

	// Robot-triage flag for direct bv triage integration
	robotTriage      bool // bv triage output
	robotTriageLimit int  // max recommendations to return

	// BV Analysis robot flags for advanced analysis modes
	robotForecast string // bv forecast analysis target (all or specific ID)
	robotSuggest  bool   // bv hygiene suggestions
	robotImpact   string // file impact analysis target
	robotSearch   string // semantic vector search query

	// BV Label robot flags for label-based analysis
	robotLabelAttention bool // attention-ranked labels by impact and urgency
	robotLabelFlow      bool // cross-label dependency flow matrix
	robotLabelHealth    bool // per-label health analysis
	robotAttentionLimit int  // max attention items to return

	// BV File robot flags for file-based analysis
	robotFileBeads          string  // beads that touched a file path
	robotFileHotspots       bool    // files touched by most beads
	robotFileRelations      string  // files that co-change with given file
	robotFileBeadsLimit     int     // max beads to return per file
	robotHotspotsLimit      int     // max hotspots to return
	robotRelationsLimit     int     // max related files to return
	robotRelationsThreshold float64 // correlation threshold (0.0-1.0)

	// Robot-restart-pane flags
	robotRestartPane            string // session name for pane restart
	robotRestartPaneBead        string // bead ID to assign after restart
	robotRestartPanePrompt      string // custom prompt to send after restart
	robotRestartPaneModel       string // model[@effort] override for the relaunch command (ntm-yusj)
	robotRestartPaneArgs        string // raw args appended to the relaunch command (ntm-yusj)
	robotRestartEnvironmentFile string // private file supplying explicit omitted launch environment

	// Robot-probe flags for active pane responsiveness testing (bd-1cu1f)
	robotProbe           string // session name to probe
	robotPaneAddress     string // session name for pane addressing cards (ntm-cac6)
	robotProbeMethod     string // probe method: keystroke_echo, interrupt_test
	robotProbeTimeout    int    // probe timeout in ms
	robotProbeAggressive bool   // fallback to interrupt_test if keystroke_echo fails

	// Robot-is-working flags for agent work state detection (bd-16ptx)
	robotIsWorking        string // session name to check
	robotIsWorkingVerbose bool   // include raw sample output

	// Semantic-progress signal flags (#199). Opt-in ground-truth signal layered
	// on --robot-is-working; default off so the poll path is unchanged.
	robotSemantic       bool   // enable the semantic_progress field
	robotSemanticWindow string // look-back window (duration, e.g. "30m"); empty = config/default

	// Robot-agent-health flags for comprehensive health check (bd-2pwzf)
	robotAgentHealth        string // session name to check
	robotAgentHealthNoCaut  bool   // skip caut provider query
	robotAgentHealthVerbose bool   // include raw sample output

	// Robot-smart-restart flags for safe agent restarts (bd-2c7f4)
	robotSmartRestart             string // session name to restart
	robotSmartRestartForce        bool   // force restart even if working
	robotSmartRestartDryRun       bool   // show what would happen without doing it
	robotSmartRestartPrompt       string // prompt to send after restart
	robotSmartRestartVerbose      bool   // include extra debugging info
	robotSmartRestartHardKill     bool   // use kill -9 fallback if soft exit fails
	robotSmartRestartHardKillOnly bool   // skip soft exit and go straight to kill -9

	// Robot-monitor flags for proactive usage limit warnings (bd-3gh5m)
	robotMonitor            string // session name to monitor
	robotMonitorInterval    string // polling interval (e.g. "30s", "1m")
	robotMonitorWarn        string // warning threshold percentage
	robotMonitorCrit        string // critical threshold percentage
	robotMonitorInfo        string // info threshold percentage
	robotMonitorAlert       string // provider usage alert threshold
	robotMonitorIncludeCaut bool   // include caut provider data
	robotMonitorOutput      string // output file path (empty = stdout)

	// Robot-support-bundle flags for diagnostic bundle generation (bd-wlon9)
	robotSupportBundle       string // session name (empty = all or none)
	robotSupportBundleOutput string // output file path
	robotSupportBundleFormat string // archive format: zip or tar.gz
	robotSupportBundleSince  string // include content since duration
	robotSupportBundleLines  int    // max lines per pane
	robotSupportBundleMax    int    // max size in MB
	robotSupportBundleRedact string // redaction mode: warn, redact, block

	// Help verbosity flags
	helpMinimal bool // show minimal help with essential commands only
	helpFull    bool // show full help (default behavior)

	// Robot-switch-account flags for CAAM account switching
	robotSwitchAccount string // provider or provider:account format

	// Robot-account-status and robot-accounts-list flags for CAAM
	robotAccountStatus         bool   // --robot-account-status flag
	robotAccountStatusProvider string // --provider filter for account-status
	robotAccountsList          bool   // --robot-accounts-list flag
	robotAccountsListProvider  string // --provider filter for accounts-list

	// Robot-env flag for environment info (bd-18gwh)
	robotEnv string // --robot-env flag (session name or "global")

	// Robot-dcg-status flag for DCG status
	robotDCGStatus         bool          // --robot-dcg-status flag
	robotDCGCheck          bool          // --robot-dcg-check / --robot-guard flag
	robotDCGCmd            string        // --command / --cmd flag (required with --robot-dcg-check / --robot-guard)
	robotDCGContext        string        // --context flag (intent/context for the command)
	robotDCGCwd            string        // --cwd flag (working directory context)
	robotSafetySimulate    bool          // --robot-safety-simulate flag
	robotSafetySteps       []string      // repeated --step values for --robot-safety-simulate
	robotPolicyDecisions   bool          // --robot-policy-decisions flag
	robotPolicyEntityType  string        // --policy-entity-type filter for --robot-policy-decisions
	robotPolicyEntityID    string        // --policy-entity-id filter for --robot-policy-decisions
	robotRelax             bool          // --relax modifier for --robot-route and --robot-assign
	robotUBSScan           string        // --robot-ubs-scan optional path
	robotScanPath          string        // --scan-path target directory for --robot-ubs-scan
	robotScanFile          string        // --scan-file single file target for --robot-ubs-scan
	robotScanStaged        bool          // --scan-staged incremental staged scan for --robot-ubs-scan
	robotScanDiff          bool          // --scan-diff incremental working-tree diff scan for --robot-ubs-scan
	robotScanOnly          []string      // --scan-only languages filter for --robot-ubs-scan
	robotScanExclude       []string      // --scan-exclude languages filter for --robot-ubs-scan
	robotScanFailOnWarning bool          // --scan-fail-on-warning health gate for --robot-ubs-scan
	robotScanTimeout       time.Duration // --scan-timeout duration for --robot-ubs-scan

	// Robot-slb flags for SLB approvals
	robotSLBPending bool   // --robot-slb-pending flag
	robotSLBApprove string // --robot-slb-approve flag
	robotSLBDeny    string // --robot-slb-deny flag
	slbReason       string // --reason (required with --robot-slb-deny)

	// Robot-ru-sync flag for RU
	robotRUSync bool // --robot-ru-sync flag

	// Robot-giil-fetch flag for GIIL
	robotGiilFetch string // --robot-giil-fetch flag

	// Robot-quota-status and robot-quota-check flags for caut
	robotQuotaStatus        bool   // --robot-quota-status flag
	robotQuotaCheck         bool   // --robot-quota-check flag
	robotQuotaCheckProvider string // --provider filter for quota-check

	// Robot-rano-stats flag for rano network stats
	robotRanoStats  bool   // --robot-rano-stats flag
	robotRanoWindow string // --rano-window for stats window

	// Robot-rch-status and robot-rch-workers flags for RCH
	robotRCHStatus   bool   // --robot-rch-status flag
	robotProxyStatus bool   // --robot-proxy-status flag
	robotRCHWorkers  bool   // --robot-rch-workers flag
	robotRCHWorker   string // --worker filter for --robot-rch-workers

	// Robot-context-inject flags for context file injection (bd-972v)
	robotContextInject      string // --robot-context-inject flag (session name)
	robotContextInjectFiles string // --inject-files flag (comma-separated file list)
	robotContextInjectMax   int    // --inject-max-bytes flag (max content size)
	robotContextInjectAll   bool   // --inject-all flag (include user pane)
	robotContextInjectPane  int    // --inject-pane flag (specific pane, -1 = all agents)
	robotContextInjectDry   bool   // --inject-dry-run flag (preview without sending)

	// Robot-mail-check flags for Agent Mail inbox integration (bd-adgv)
	robotMailCheck    bool   // --robot-mail-check flag
	mailProject       string // --project for mail check (required)
	mailAgent         string // --agent filter for specific agent inbox
	mailThread        string // --thread filter for specific thread
	mailStatus        string // --status filter: read, unread, all
	mailIncludeBodies bool   // --include-bodies flag
	mailUrgentOnly    bool   // --urgent-only flag
	mailVerbose       bool   // --verbose flag for extra details
	mailOffset        int    // --mail-offset for pagination
	mailUntil         string // --mail-until date filter (YYYY-MM-DD)
)

func registerRobotFlags() {
	// Robot flags for AI agents - state inspection commands
	rootCmd.Flags().BoolVar(&robotHelp, "robot-help", false, "Show comprehensive human-readable AI agent integration guide with examples")
	rootCmd.Flags().BoolVar(&robotStatus, "robot-status", false, "Session headers (name, attached, agent_count, health) plus summary and alerts — not per-pane detail; use --robot-snapshot for .sessions[].agents[]. Start here. Example: ntm --robot-status")
	rootCmd.Flags().BoolVar(&robotVersion, "robot-version", false, "Get ntm version, commit, build info (JSON). Example: ntm --robot-version")
	rootCmd.Flags().BoolVar(&robotCapabilities, "robot-capabilities", false, "Get all available robot commands with parameters and descriptions (JSON). Machine-discoverable API")
	rootCmd.Flags().StringVar(&robotCapabilitiesCommand, "capability-command", "", "Filter --robot-capabilities to one exact command name or flag")
	rootCmd.Flags().StringVar(&robotCapabilitiesCategory, "capability-category", "", "Filter --robot-capabilities to one exact category")
	rootCmd.Flags().StringVar(&robotCapabilitiesSearch, "capability-search", "", "Search --robot-capabilities metadata for a case-insensitive query")
	rootCmd.Flags().BoolVar(&robotCapabilitiesCompact, "capability-compact", false, "Return a token-efficient --robot-capabilities command catalog")
	rootCmd.Flags().StringVar(&robotDocs, "robot-docs", "", "Get documentation for a topic (JSON). Topics: quickstart, commands, examples, exit-codes. Example: ntm --robot-docs=quickstart")
	rootCmd.Flags().BoolVar(&robotPlan, "robot-plan", false, "Get bv execution plan with parallelizable tracks (JSON). Example: ntm --robot-plan")
	rootCmd.Flags().BoolVar(&robotSnapshot, "robot-snapshot", false, "Unified state: sessions + beads + alerts + mail. Use --since for delta. Example: ntm --robot-snapshot")
	rootCmd.Flags().StringVar(&robotSince, "since", "", "Shared time filter for commands that support --since. Snapshot uses RFC3339; history/diff/summary accept duration or RFC3339 timestamps; tokens accepts ISO8601 or YYYY-MM-DD; robot-mail-check accepts YYYY-MM-DD. Examples: --since=2025-12-15T10:00:00Z, --since=1h, --since=2025-12-15")
	rootCmd.Flags().BoolVar(&robotEvents, "robot-events", false, "Stream attention events since cursor. Use for raw replay/feed. Example: ntm --robot-events --since-cursor=42 --events-limit=50")
	rootCmd.Flags().Int64Var(&robotEventsSinceCursor, "since-cursor", 0, "Cursor position to replay from. Optional with --robot-events. Example: --since-cursor=42")
	rootCmd.Flags().IntVar(&robotEventsLimit, "events-limit", 100, "Max events to return. Optional with --robot-events. Example: --events-limit=50")
	rootCmd.Flags().StringVar(&robotEventsIncident, "events-incident", "", "Durable incident ID for bounded historical replay. Optional with --robot-events. Example: --events-incident=inc-20260322-abc")
	rootCmd.Flags().StringVar(&robotEventsAsOf, "events-as-of", "", "RFC3339 timestamp for bounded historical reconstruction. Optional with --robot-events. Example: --events-as-of=2026-03-22T03:50:00Z")
	rootCmd.Flags().StringVar(&robotEventsWindowBefore, "events-window-before", "", "Replay context before incident start. Optional with --robot-events --events-incident. Example: --events-window-before=5m")
	rootCmd.Flags().StringVar(&robotEventsWindowAfter, "events-window-after", "", "Replay context after incident end. Optional with --robot-events --events-incident. Example: --events-window-after=1m")
	rootCmd.Flags().StringVar(&robotEventsCategory, "events-category", "", "Filter by event category. Optional with --robot-events. Example: --events-category=agent")
	rootCmd.Flags().StringVar(&robotEventsSession, "events-session", "", "Filter by session name. Optional with --robot-events. Example: --events-session=myproject")
	rootCmd.Flags().StringVar(&robotEventsActionability, "events-actionability", "", "Filter by actionability level. Optional with --robot-events. Values: action_required, interesting, background")
	rootCmd.Flags().StringVar(&robotProfile, "profile", "", "Attention-feed filter profile. Applies to --robot-events, --robot-attention, --robot-digest, --robot-wait. Values: operator, debug, minimal, alerts")
	rootCmd.Flags().BoolVar(&robotAttention, "robot-attention", false, "The one obvious tending primitive: wait for attention, then return digest. Example: ntm --robot-attention --attention-cursor=42")
	rootCmd.Flags().BoolVar(&robotDigest, "robot-digest", false, "Non-blocking attention digest. Returns counts and top items without waiting. Example: ntm --robot-digest --profile=minimal")
	rootCmd.Flags().Int64Var(&robotAttentionSinceCursor, "attention-cursor", 0, "Cursor position to wait/digest from. Optional with --robot-attention. Example: --attention-cursor=42")
	rootCmd.Flags().StringVar(&robotAttentionSession, "attention-session", "", "Filter to specific session. Optional with --robot-attention. Example: --attention-session=myproject")
	rootCmd.Flags().StringVar(&robotAttentionTimeout, "attention-timeout", "5m", "Maximum wait time. Optional with --robot-attention. Example: --attention-timeout=10m")
	rootCmd.Flags().StringVar(&robotAttentionPoll, "attention-poll", "1s", "Polling interval. Optional with --robot-attention. Example: --attention-poll=500ms")
	rootCmd.Flags().StringVar(&robotAttentionCondition, "attention-condition", "attention", "Which condition to wait for. Optional with --robot-attention. Values: attention, action_required, mail_pending")
	rootCmd.Flags().StringVar(&robotTail, "robot-tail", "", "Capture recent pane output. Required: SESSION. Optional: --lines, --panes, --fresh. Example: ntm --robot-tail=myproject --lines=50")
	rootCmd.Flags().BoolVar(&robotTailFresh, "fresh", false, "Force a direct live capture per pane instead of buffered observer content (ground truth immediately after an action). Use with --robot-tail")
	rootCmd.Flags().StringVar(&robotWatchBead, "robot-watch-bead", "", "Capture bead mentions across panes plus current bead status (JSON snapshot). Required: SESSION")
	rootCmd.Flags().StringVar(&robotWatchBeadID, "bead", "", "Bead ID for --robot-watch-bead. Example: --bead=bd-abc123")
	rootCmd.Flags().StringVar(&robotErrors, "robot-errors", "", "Filter pane output to show only errors. Required: SESSION. Example: ntm --robot-errors=myproject --lines=100")
	rootCmd.Flags().IntVar(&robotLines, "lines", 20, "Lines to capture per pane. Optional with --robot-tail, --robot-errors, --robot-watch-bead, --robot-is-working, --robot-agent-health, --robot-smart-restart, and --robot-monitor. Example: --lines=100")
	rootCmd.Flags().StringVar(&robotPanes, "panes", "", "Filter with comma-separated N, W.P, or %N pane selectors. Optional with --robot-tail, --robot-watch-bead, --robot-errors, --robot-send, --robot-ack, --robot-interrupt, --robot-wait, --robot-is-working, --robot-agent-health, --robot-smart-restart, --robot-restart-pane, --robot-probe, and --robot-monitor. Example: --panes=1,2.0,%7")
	rootCmd.Flags().StringVar(&robotIsWorking, "robot-is-working", "", "Check if agents are working. Returns work state with recommendations. Required: SESSION. Example: ntm --robot-is-working=myproject --panes=2,3")
	rootCmd.Flags().BoolVar(&robotIsWorkingVerbose, "is-working-verbose", false, "Include raw sample output in --robot-is-working response. Example: --is-working-verbose")
	rootCmd.Flags().BoolVar(&robotSemantic, "semantic", false, "Add an optional ground-truth semantic_progress field to --robot-is-working (token-attributed git commits / bead claims). Advisory only; never flips is_working. Off by default (no git/br calls). Example: ntm --robot-is-working=myproject --semantic")
	rootCmd.Flags().StringVar(&robotSemanticWindow, "semantic-window", "", "Look-back window for --semantic (Go duration, e.g. 30m, 2h). Defaults to [robot.semantic].window_minutes or 30m. Example: --semantic --semantic-window=1h")
	rootCmd.Flags().StringVar(&robotAgentHealth, "robot-agent-health", "", "Comprehensive agent health check combining local state and provider usage. Required: SESSION. Example: ntm --robot-agent-health=myproject --panes=2,3")
	rootCmd.Flags().BoolVar(&robotAgentHealthNoCaut, "no-caut", false, "Skip caut provider query for faster local-only health check. Optional with --robot-agent-health")
	rootCmd.Flags().BoolVar(&robotAgentHealthVerbose, "agent-health-verbose", false, "Include raw sample output in --robot-agent-health response. Example: --agent-health-verbose")
	rootCmd.Flags().StringVar(&robotSmartRestart, "robot-smart-restart", "", "SAFE restart: checks --robot-is-working first, refuses to interrupt working agents. Required: SESSION. Example: ntm --robot-smart-restart=myproject --panes=2,3")
	rootCmd.Flags().BoolVar(&robotSmartRestartForce, "force", false, "DANGEROUS: Force execution for commands that support it. With --robot-smart-restart, restart even if an agent appears to be working. With --robot-interrupt, send Ctrl+C even if an agent already looks ready. Use with extreme caution!")
	rootCmd.Flags().BoolVar(&robotSmartRestartDryRun, "smart-restart-dry-run", false, "Show what would happen without performing restart. Optional with --robot-smart-restart")
	rootCmd.Flags().StringVar(&robotSmartRestartPrompt, "prompt", "", "Send this prompt to the agent after restart. Optional with --robot-smart-restart")
	rootCmd.Flags().BoolVar(&robotSmartRestartVerbose, "smart-restart-verbose", false, "Include extra debugging info in --robot-smart-restart response")
	rootCmd.Flags().BoolVar(&robotSmartRestartHardKill, "hard-kill", false, "Use kill -9 fallback if the agent ignores the normal exit sequence. Optional with --robot-smart-restart")
	rootCmd.Flags().BoolVar(&robotSmartRestartHardKillOnly, "hard-kill-only", false, "Skip the normal exit sequence and go straight to kill -9. Optional with --robot-smart-restart")
	rootCmd.Flags().StringVar(&robotMonitor, "robot-monitor", "", "Start proactive monitoring for usage limits. Emits JSONL warnings. Required: SESSION. Example: ntm --robot-monitor=myproject --interval=30s")
	rootCmd.Flags().StringVar(&robotMonitorInterval, "interval", "", "Polling interval for --robot-monitor and status polling for --robot-watch-bead. Example: --interval=30s")
	rootCmd.Flags().StringVar(&robotMonitorWarn, "warn-threshold", "", "Context % for WARNING level. Optional with --robot-monitor. Example: --warn-threshold=25 (default 25)")
	rootCmd.Flags().StringVar(&robotMonitorCrit, "crit-threshold", "", "Context % for CRITICAL level. Optional with --robot-monitor. Example: --crit-threshold=15 (default 15)")
	rootCmd.Flags().StringVar(&robotMonitorInfo, "info-threshold", "", "Context % for INFO level. Optional with --robot-monitor. Example: --info-threshold=40 (default 40)")
	rootCmd.Flags().StringVar(&robotMonitorAlert, "alert-threshold", "", "Provider usage % for ALERT level. Optional with --robot-monitor. Example: --alert-threshold=80 (default 80)")
	rootCmd.Flags().BoolVar(&robotMonitorIncludeCaut, "include-caut", false, "Query caut for provider usage data. Optional with --robot-monitor")
	rootCmd.Flags().StringVar(&robotMonitorOutput, "output", "", "Output file path. Optional with --robot-monitor and --robot-save. Example: --output=/tmp/ntm-output.json")

	// Robot-support-bundle flags for diagnostic bundle generation
	rootCmd.Flags().StringVar(&robotSupportBundle, "robot-support-bundle", "", "Generate support bundle with diagnostic info. Optional: SESSION. Example: ntm --robot-support-bundle=myproject")
	rootCmd.Flags().StringVar(&robotSupportBundleOutput, "bundle-output", "", "Output file path for bundle. Optional with --robot-support-bundle. Example: --bundle-output=/tmp/debug.zip")
	rootCmd.Flags().StringVar(&robotSupportBundleFormat, "bundle-format", "zip", "Archive format: zip or tar.gz. Optional with --robot-support-bundle")
	rootCmd.Flags().StringVar(&robotSupportBundleSince, "bundle-since", "", "Include content from duration ago. Optional with --robot-support-bundle. Example: --bundle-since=1h")
	rootCmd.Flags().IntVar(&robotSupportBundleLines, "bundle-lines", 1000, "Max scrollback lines per pane. Optional with --robot-support-bundle. Example: --bundle-lines=500")
	rootCmd.Flags().IntVar(&robotSupportBundleMax, "bundle-max-size", 100, "Max bundle size in MB. Optional with --robot-support-bundle. Example: --bundle-max-size=50")
	rootCmd.Flags().StringVar(&robotSupportBundleRedact, "bundle-redact", "redact", "Redaction mode: off, warn, redact, block. Optional with --robot-support-bundle")

	rootCmd.Flags().BoolVar(&robotGraph, "robot-graph", false, "Get bv dependency graph insights: PageRank, critical path, cycles (JSON)")
	rootCmd.Flags().BoolVar(&robotTriage, "robot-triage", false, "Get bv triage analysis with recommendations, quick wins, blockers (JSON). Example: ntm --robot-triage --limit=20")
	rootCmd.Flags().IntVar(&robotTriageLimit, "triage-limit", 10, "Max recommendations per category. Optional with --robot-triage. Example: --triage-limit=20")
	rootCmd.Flags().BoolVar(&robotDashboard, "robot-dashboard", false, "Get dashboard summary as markdown (or JSON with --json). Token-efficient overview")
	rootCmd.Flags().StringVar(&robotContext, "robot-context", "", "Get context window usage for all agents in a session. Required: SESSION. Example: ntm --robot-context=myproject")
	rootCmd.Flags().BoolVar(&robotEnsembleModesList, "robot-ensemble-modes", false, "List reasoning modes (JSON). Optional: --tier, --category, --limit, --offset")
	rootCmd.Flags().BoolVar(&robotEnsemblePresetsList, "robot-ensemble-presets", false, "List ensemble presets (JSON). Example: ntm --robot-ensemble-presets")
	rootCmd.Flags().StringVar(&robotEnsemble, "robot-ensemble", "", "Get ensemble state for a session. Required: SESSION. Example: ntm --robot-ensemble=myproject")
	rootCmd.Flags().StringVar(&robotEnsembleSpawn, "robot-ensemble-spawn", "", "Spawn a reasoning ensemble. Required: SESSION. Example: ntm --robot-ensemble-spawn=myproject --preset=project-diagnosis --question='...'")
	rootCmd.Flags().StringVar(&robotEnsemblePreset, "preset", "", "Ensemble preset name. Required with --robot-ensemble-spawn unless --modes is set")
	rootCmd.Flags().StringVar(&robotEnsembleModes, "modes", "", "Explicit mode IDs or codes (comma-separated). Used with --robot-ensemble-spawn")
	rootCmd.Flags().StringVar(&robotEnsembleQuestion, "question", "", "Question for ensemble spawn. Required with --robot-ensemble-spawn")
	rootCmd.Flags().StringVar(&robotEnsembleAgents, "agents", "", "Agent mix for ensemble spawn (e.g., cc=2,cod=1,agy=1)")
	rootCmd.Flags().StringVar(&robotEnsembleAssignment, "assignment", "affinity", "Assignment strategy for ensemble spawn: round-robin, affinity, category, explicit")
	rootCmd.Flags().BoolVar(&robotEnsembleAllowAdvanced, "allow-advanced", false, "Allow advanced/experimental modes with --robot-ensemble-spawn")
	rootCmd.Flags().IntVar(&robotEnsembleBudgetTotal, "budget-total", 0, "Override total token budget for ensemble spawn")
	rootCmd.Flags().IntVar(&robotEnsembleBudgetPerMode, "budget-per-agent", 0, "Override per-agent token cap for ensemble spawn")
	rootCmd.Flags().BoolVar(&robotEnsembleNoCache, "no-cache", false, "Bypass context cache for ensemble spawn")
	rootCmd.Flags().StringVar(&robotEnsembleProject, "project", "", "Project directory override for robot commands (e.g., ensemble spawn, JFP install)")
	rootCmd.Flags().StringVar(&robotEnsembleSuggest, "robot-ensemble-suggest", "", "Suggest best ensemble preset for a question. Example: ntm --robot-ensemble-suggest=\"What security issues exist?\"")
	rootCmd.Flags().BoolVar(&robotEnsembleSuggestIDOnly, "suggest-id-only", false, "Output only preset name with --robot-ensemble-suggest")
	rootCmd.Flags().StringVar(&robotEnsembleStop, "robot-ensemble-stop", "", "Stop an ensemble and save partial state. Required: SESSION. Example: ntm --robot-ensemble-stop=myproject")
	rootCmd.Flags().BoolVar(&robotEnsembleStopForce, "stop-force", false, "Force kill without graceful shutdown. Optional with --robot-ensemble-stop")
	rootCmd.Flags().BoolVar(&robotEnsembleStopNoCollect, "stop-no-collect", false, "Skip partial output collection. Optional with --robot-ensemble-stop")

	// Overlay flags for agent-initiated human handoff (br-a6cmp)
	rootCmd.Flags().BoolVar(&robotOverlay, "robot-overlay", false, "Open dashboard overlay for human handoff (JSON). Requires tmux. Example: ntm --robot-overlay")
	rootCmd.Flags().StringVar(&robotOverlaySession, "overlay-session", "", "Session for overlay. Optional with --robot-overlay, defaults to current session. Example: --overlay-session=myproject")
	rootCmd.Flags().Int64Var(&robotOverlayCursor, "overlay-cursor", 0, "Pre-focus attention panel on this cursor. Optional with --robot-overlay. Example: --overlay-cursor=42")
	rootCmd.Flags().BoolVar(&robotOverlayNoWait, "overlay-no-wait", false, "Return immediately without blocking. Optional with --robot-overlay")

	rootCmd.Flags().IntVar(&robotBeadLimit, "bead-limit", 5, "Max beads per category in snapshot. Optional with --robot-snapshot, --robot-status. Example: --bead-limit=10")
	rootCmd.Flags().IntVar(&robotLimit, "robot-limit", 0, "Max items to return for robot list outputs (status, snapshot, history). Example: --robot-limit=10")
	rootCmd.Flags().IntVar(&robotOffset, "robot-offset", 0, "Pagination offset for robot list outputs (status, snapshot, history). Example: --robot-offset=20")
	rootCmd.Flags().StringVar(&robotVerbosity, "robot-verbosity", "", "Robot verbosity profile for JSON/TOON: terse, default, or debug. Env: NTM_ROBOT_VERBOSITY")

	// BV Analysis robot flags for advanced analysis modes
	rootCmd.Flags().StringVar(&robotForecast, "robot-forecast", "", "Get ETA predictions. Use 'all' or specific ID. Example: ntm --robot-forecast=br-123")
	rootCmd.Flags().BoolVar(&robotSuggest, "robot-suggest", false, "Get hygiene suggestions: duplicates, missing deps, label suggestions (JSON)")
	rootCmd.Flags().StringVar(&robotImpact, "robot-impact", "", "Get file impact analysis. Required: FILE_PATH. Example: ntm --robot-impact=src/main.go")
	rootCmd.Flags().StringVar(&robotSearch, "robot-search", "", "Semantic vector search. Required: QUERY. Example: ntm --robot-search='authentication bug'")

	// BV Label robot flags for label-based analysis
	rootCmd.Flags().BoolVar(&robotLabelAttention, "robot-label-attention", false, "Get attention-ranked labels by impact and urgency (JSON)")
	rootCmd.Flags().IntVar(&robotAttentionLimit, "attention-limit", 10, "Max attention items to return. Optional with --robot-label-attention")
	rootCmd.Flags().BoolVar(&robotLabelFlow, "robot-label-flow", false, "Get cross-label dependency flow matrix and bottleneck analysis (JSON)")
	rootCmd.Flags().BoolVar(&robotLabelHealth, "robot-label-health", false, "Get per-label health analysis: velocity, staleness, blocked count (JSON)")

	// BV File robot flags for file-based analysis
	rootCmd.Flags().StringVar(&robotFileBeads, "robot-file-beads", "", "Get beads that touched a file path. Required: FILE_PATH. Example: ntm --robot-file-beads=src/main.go")
	rootCmd.Flags().IntVar(&robotFileBeadsLimit, "file-beads-limit", 20, "Max beads to return per file. Optional with --robot-file-beads")
	rootCmd.Flags().BoolVar(&robotFileHotspots, "robot-file-hotspots", false, "Get files touched by most beads (quality hotspots) (JSON)")
	rootCmd.Flags().IntVar(&robotHotspotsLimit, "hotspots-limit", 10, "Max hotspots to return. Optional with --robot-file-hotspots")
	rootCmd.Flags().StringVar(&robotFileRelations, "robot-file-relations", "", "Get files that co-change with given file. Required: FILE_PATH. Example: ntm --robot-file-relations=src/api.go")
	rootCmd.Flags().IntVar(&robotRelationsLimit, "relations-limit", 10, "Max related files to return. Optional with --robot-file-relations")
	rootCmd.Flags().Float64Var(&robotRelationsThreshold, "relations-threshold", 0.5, "Correlation threshold (0.0-1.0). Optional with --robot-file-relations")

	// Robot-send flags for batch messaging
	rootCmd.Flags().StringVar(&robotSend, "robot-send", "", "Send message to panes atomically. Required: SESSION, --msg or --msg-file. Example: ntm --robot-send=proj --msg='Fix auth'")
	rootCmd.Flags().StringVar(&robotSendMsg, "msg", "", "Shared message payload. Required with --robot-send unless --msg-file is set. Optional with --robot-ack (echo detection), --robot-interrupt (post-interrupt retask), and --robot-route (the message being routed; required with --strategy=affinity)")
	rootCmd.Flags().StringVar(&robotSendMsgFile, "msg-file", "", "Read message content from file, or stdin with '-'. Keeps prompt text out of the scanned command line so command-safety filters cannot mistake message content for executable commands. Use with --robot-send or --robot-route")
	rootCmd.Flags().BoolVar(&robotSendEnter, "enter", true, "Send Enter after pasting message (default: true). Use --enter=false to paste without submitting")
	rootCmd.Flags().BoolVar(&robotSendEnter, "submit", true, "Alias for --enter")
	// Field evidence across multiple machines shows agents reliably guess
	// --message before finding --msg; normalize the alias instead of burning
	// an INVALID_FLAG round-trip per session.
	rootCmd.Flags().SetNormalizeFunc(func(f *pflag.FlagSet, name string) pflag.NormalizedName {
		if name == "message" {
			name = "msg"
		}
		return pflag.NormalizedName(name)
	})
	rootCmd.Flags().BoolVar(&robotSendAll, "all", false, "Include user pane (default: agents only). Optional with --robot-send, --robot-interrupt, --robot-restart-pane, and --robot-support-bundle")
	// Accepted-and-ignored for parity with `ntm send` (ntm-dv50 / AP-51):
	// operators habitually carry --no-cass-check on every dispatch, and the
	// robot parser rejecting a flag the shell parser requires forced a
	// per-surface flag lookup table. Robot sends never run the interactive
	// CASS confirm, so the flag is a documented no-op here.
	var robotNoCassCheckCompat bool
	rootCmd.Flags().BoolVar(&robotNoCassCheckCompat, "no-cass-check", false, "Accepted for parity with 'ntm send'; robot commands never run the interactive CASS duplicate check (no-op)")
	rootCmd.Flags().StringVar(&robotSendType, "type", "", "Filter by agent type: claude|cc, codex|cod, antigravity|agy, gemini|gmi (legacy), cursor, windsurf, aider. Works with --robot-history, --robot-wait, --robot-route, --robot-send, --robot-ack, --robot-interrupt, --robot-errors, and --robot-restart-pane")
	rootCmd.Flags().StringVar(&robotSendExclude, "exclude", "", "Exclude pane indices (comma-separated). Optional with --robot-send and --robot-route. Example: --exclude=0,3")
	rootCmd.Flags().IntVar(&robotSendDelay, "delay-ms", 0, "Delay between sends (ms). Optional with --robot-send. Example: --delay-ms=500 for 0.5s between panes")
	rootCmd.Flags().BoolVar(&robotSendClearInput, "clear-input", false, "Clear residual composer text (agent-specific keys, verified) before typing. Optional with --robot-send; recommended after interrupts on codex panes")
	rootCmd.Flags().BoolVar(&robotSendVerifyRender, "verify-render", false, "Capture bounded before/after pane output and require rendered delivery evidence. Optional with --robot-send")
	rootCmd.Flags().BoolVar(&robotSendWithMemory, "with-memory", false, "Inject relevant CM (cass-memory) rules above the message before sending. Optional with --robot-send, --robot-bulk-assign and --robot-spawn --spawn-assign-work (each fresh assignment prompt); degrades gracefully when cm is unavailable. Config: [memory] send_injection/send_max_rules/send_budget_tokens")
	rootCmd.Flags().BoolVar(&robotSendWithCASS, "with-cass", false, "Inject relevant CASS session context above the message before sending. Optional with --robot-send, --robot-bulk-assign and --robot-spawn --spawn-assign-work (each fresh assignment prompt, queried by bead title/labels/description); degrades gracefully when cass is unavailable. Config: [cass.context] enabled/max_sessions/lookback_days/max_tokens/min_relevance/skip_if_context_above/prefer_same_project")
	rootCmd.Flags().BoolVar(&robotSendNoCASS, "no-cass", false, "Disable CASS context injection for this send or bulk assignment, overriding [cass.context] enabled=true")
	rootCmd.Flags().StringVar(&robotOpID, "op-id", "", "Durable idempotent operation ID for --robot-send and --robot-interrupt: identical retries replay the recorded outcome without touching panes again, conflicting reuse is rejected. Example: ntm --robot-interrupt=proj --msg='...' --op-id=retask-42")
	rootCmd.Flags().StringVar(&robotSendReceipt, "robot-send-receipt", "", "Query the durable receipt of an idempotent --robot-send or --robot-interrupt by operation ID. Example: ntm --robot-send-receipt=deploy-42")
	rootCmd.Flags().StringVar(&robotActionStatus, "robot-action-status", "", "Inspect lifecycle status of a durable robot operation or wait handle by request ID. Example: ntm --robot-action-status=deploy-42")
	rootCmd.Flags().Lookup("robot-action-status").NoOptDefVal = "__present__"
	rootCmd.Flags().StringVar(&robotWaitRequest, "robot-wait-request", "", "Wait for a durable robot operation or wait handle to reach a terminal state. Example: ntm --robot-wait-request=deploy-42 --timeout=30s")
	rootCmd.Flags().Lookup("robot-wait-request").NoOptDefVal = "__present__"
	rootCmd.Flags().StringVar(&robotCancelRequest, "robot-cancel-request", "", "Cancel a pre-dispatch durable operation claim or active wait handle by request ID. Example: ntm --robot-cancel-request=deploy-42")
	rootCmd.Flags().Lookup("robot-cancel-request").NoOptDefVal = "__present__"
	rootCmd.Flags().StringVar(&robotRequestID, "request-id", "", "Explicit request or operation ID selector for --robot-action-status, --robot-wait-request, and --robot-cancel-request")

	// Robot-assign flags for work distribution
	rootCmd.Flags().StringVar(&robotAssign, "robot-assign", "", "Get work distribution recommendations. Required: SESSION. Example: ntm --robot-assign=proj --strategy=speed")
	rootCmd.Flags().StringVar(&robotAssignBeads, "beads", "", "Specific bead IDs to assign (comma-separated). Optional with --robot-assign. Example: --beads=ntm-abc,ntm-xyz")
	rootCmd.Flags().StringVar(&robotAssignStrategy, "strategy", "simple", "Strategy override for commands that support it. --robot-assign: simple (sequential pairing), balanced, speed, quality, dependency (graph-aware planner). --robot-route: least-loaded, first-available, round-robin, round-robin-available, random, sticky, explicit, affinity (needs --msg). --robot-spawn with --spawn-assign-work: top-n, diverse, dependency-aware, skill-matched.")
	rootCmd.Flags().BoolVar(&robotAssignRelax, "relax", false, "With --robot-assign, retry an empty scored plan at half its score floor without weakening availability, context, capacity, or safety gates. With --robot-route, include THINKING agents when idle agents are insufficient")

	// Robot-bulk-assign flags for batch work distribution
	rootCmd.Flags().StringVar(&robotBulkAssign, "robot-bulk-assign", "", "Bulk assign beads to all idle agents. Required: SESSION. Example: ntm --robot-bulk-assign=proj --from-bv")
	rootCmd.Flags().BoolVar(&robotBulkAssignFromBV, "from-bv", false, "Use bv triage for bead selection. Use with --robot-bulk-assign")
	rootCmd.Flags().StringVar(&robotBulkAssignAlloc, "allocation", "", "Explicit pane->bead allocation JSON. Alternative to --from-bv. Example: --allocation='{\"2\":\"bd-abc\"}'")
	rootCmd.Flags().StringVar(&robotBulkAssignStrategy, "bulk-strategy", "impact", "Bulk assignment strategy: impact (default), ready, stale, balanced. Use with --from-bv")
	rootCmd.Flags().StringVar(&robotBulkAssignSkip, "skip-panes", "", "Comma-separated N, W.P, or %N pane selectors to skip. Use with --robot-bulk-assign")
	rootCmd.Flags().StringVar(&robotBulkAssignTemplate, "prompt-template", "", "Custom prompt template file. Use with --robot-bulk-assign")
	rootCmd.Flags().BoolVar(&robotBulkAssignParallel, "bulk-parallel", false, "Execute independent bulk assignments concurrently")
	rootCmd.Flags().DurationVar(&robotBulkAssignStagger, "bulk-stagger", 0, "Delay between sequential bulk assignments, for example 500ms")
	rootCmd.Flags().BoolVar(&robotReserveFiles, "reserve-files", true, "Reserve each --robot-bulk-assign bead's file scope in Agent Mail after its claim and before dispatch, as 'ntm assign' does: --reservation-paths when given, otherwise the paths named in the bead's title and description. A bead that names no files, or an unavailable Agent Mail, refuses that assignment unclaimed. --reserve-files=false opts out")
	rootCmd.Flags().BoolVar(&robotRequireReservation, "require-reservation", false, "Require exact Agent Mail file reservations before --robot-spawn work assignment (--robot-bulk-assign reserves by default; see --reserve-files)")
	rootCmd.Flags().StringVar(&robotReservationPaths, "reservation-paths", "", "Comma-separated project-relative file globs to reserve before bulk/spawn work assignment; for --robot-bulk-assign they replace the paths discovered from each bead")

	// Robot-health flag for session/project health summary
	rootCmd.Flags().StringVar(&robotHealth, "robot-health", "", "Get session or project health (JSON). SESSION for per-agent health, empty for project health. Example: ntm --robot-health=myproject")
	rootCmd.Flags().Lookup("robot-health").NoOptDefVal = "__present__"
	rootCmd.Flags().StringVar(&robotHealthOAuth, "robot-health-oauth", "", "Get per-agent OAuth and rate-limit status (JSON). Required: SESSION. Example: ntm --robot-health-oauth=myproject")

	// Robot-health-restart-stuck flags for auto-restarting stuck agents
	rootCmd.Flags().StringVar(&robotHealthRestartStuck, "robot-health-restart-stuck", "", "Detect and restart stuck agents (no output for N minutes). Required: SESSION. Example: ntm --robot-health-restart-stuck=myproject")
	rootCmd.Flags().StringVar(&robotStuckThreshold, "stuck-threshold", "", "Duration before considering agent stuck (default 5m). Use with --robot-health-restart-stuck. Example: --stuck-threshold=10m")

	// Robot-logs flags for aggregated agent logs
	rootCmd.Flags().StringVar(&robotLogs, "robot-logs", "", "Get aggregated logs from all agent panes (JSON). Required: SESSION. Example: ntm --robot-logs=myproject")
	rootCmd.Flags().StringVar(&robotLogsPanes, "logs-panes", "", "Filter to specific pane indices (comma-separated). Use with --robot-logs. Example: --logs-panes=1,2,3")
	rootCmd.Flags().IntVar(&robotLogsLimit, "logs-limit", 100, "Max lines per pane. Use with --robot-logs. Example: --logs-limit=50")

	// Robot-diagnose flags for comprehensive health diagnosis
	rootCmd.Flags().StringVar(&robotDiagnose, "robot-diagnose", "", "Comprehensive health check with fix recommendations. Required: SESSION. Example: ntm --robot-diagnose=myproject")
	rootCmd.Flags().BoolVar(&robotDiagnoseFix, "diagnose-fix", false, "Attempt auto-fix for fixable issues. Use with --robot-diagnose. Example: --robot-diagnose=proj --diagnose-fix")
	rootCmd.Flags().BoolVar(&robotDiagnoseBrief, "diagnose-brief", false, "Minimal output (summary only). Use with --robot-diagnose")
	rootCmd.Flags().IntVar(&robotDiagnosePane, "diagnose-pane", -1, "Diagnose specific pane only. Use with --robot-diagnose. Example: --diagnose-pane=2")

	// Robot-recipes flag for recipe listing
	rootCmd.Flags().BoolVar(&robotRecipes, "robot-recipes", false, "List available spawn recipes/presets (JSON). Use with --robot-spawn --spawn-preset")

	// Robot-schema flag for JSON Schema generation
	rootCmd.Flags().StringVar(&robotSchema, "robot-schema", "", "Generate JSON Schema for a robot response type. Required: TYPE. Use TYPE=all for every available schema.")
	rootCmd.Flags().StringVar(&robotSchema, "schema", "", "Alias for --robot-schema. Generate JSON Schema for a robot response type")

	// Robot-mail flag for Agent Mail state
	rootCmd.Flags().BoolVar(&robotMail, "robot-mail", false, "Get Agent Mail inbox/outbox state (JSON). Shows pending messages and coordination status")

	// Robot-tools flag for tool inventory and health
	rootCmd.Flags().BoolVar(&robotTools, "robot-tools", false, "Get tool inventory with health status (JSON). Shows all registered flywheel tools")

	// Robot-acfs/setup flags for setup status
	rootCmd.Flags().BoolVar(&robotACFSStatus, "robot-acfs-status", false, "Get setup status via ACFS (JSON)")
	rootCmd.Flags().BoolVar(&robotSetup, "robot-setup", false, "Alias for --robot-acfs-status")

	// Robot-ack flags for send confirmation tracking
	rootCmd.Flags().StringVar(&robotAck, "robot-ack", "", "Watch for agent responses after send. Required: SESSION. Example: ntm --robot-ack=proj --timeout=30s")
	rootCmd.Flags().StringVar(&robotAckTimeout, "ack-timeout", "30s", "Max wait time for responses (e.g., 30s, 5000ms, 1m). Works with --robot-ack, --track")
	rootCmd.Flags().IntVar(&robotAckPoll, "ack-poll", 500, "Poll interval in ms. Optional with --robot-ack. Lower = faster detection, higher CPU")
	rootCmd.Flags().BoolVar(&robotAckTrack, "track", false, "Combined send+ack: send --msg and wait for response. Use with --robot-send. Example: ntm --robot-send=proj --msg='hello' --track")

	// Robot-spawn flags for structured session creation
	rootCmd.Flags().StringVar(&robotSpawn, "robot-spawn", "", "Create session with agents. Required: SESSION name. Example: ntm --robot-spawn=myproject --spawn-cc=2")
	rootCmd.Flags().StringVar(&robotSpawnCC, "spawn-cc", "", "Claude Code agents to spawn: count[:model[:effort]] (effort also as model@effort). Use with --robot-spawn. Example: --spawn-cc=2 or --spawn-cc=2:opus:high")
	rootCmd.Flags().StringVar(&robotSpawnCod, "spawn-cod", "", "Codex CLI agents to spawn: count[:model[:effort]] (effort also as model@effort). Use with --robot-spawn. Example: --spawn-cod=1 or --spawn-cod=8:gpt-5.3-codex:high")
	rootCmd.Flags().StringVar(&robotSpawnGmi, "spawn-gmi", "", "Gemini CLI agents to spawn: count[:model]. Use with --robot-spawn. Example: --spawn-gmi=1")
	rootCmd.Flags().StringVar(&robotSpawnAgy, "spawn-agy", "", fmt.Sprintf("Antigravity CLI agents to spawn: count (default model %s via [models] default_antigravity). Use with --robot-spawn. Example: --spawn-agy=1", config.AntigravityRequiredModel))
	rootCmd.Flags().StringVar(&robotSpawnGrok, "spawn-grok", "", "Grok Build agents to spawn: count[:model[:effort]] (effort also as model@effort). Use with --robot-spawn. Example: --spawn-grok=1")
	rootCmd.Flags().StringVar(&robotSpawnOmp, "spawn-omp", "", "Oh My Pi (omp) agents to spawn: count[:model[:effort]] (effort maps to omp --thinking; no model = omp's own default). Use with --robot-spawn. Example: --spawn-omp=8 or --spawn-omp=2:opus:high")
	rootCmd.Flags().StringVar(&robotSpawnOc, "spawn-oc", "", "OpenCode (oc) agents to spawn: count[:model] (model is OpenCode's provider/model; no model = OpenCode's own default). Use with --robot-spawn. Example: --spawn-oc=2 or --spawn-oc=1:opencode/big-pickle")
	rootCmd.Flags().StringVar(&robotSpawnOllama, "spawn-ollama", "", "Ollama local agents to spawn: count[:model] (default model: codellama:latest). Use with --robot-spawn. Example: --spawn-ollama=2 or --spawn-ollama=1:qwen2.5-coder:32b")
	rootCmd.Flags().StringVar(&robotSpawnAider, "spawn-aider", "", "Aider agents to spawn: count[:model]. Use with --robot-spawn. Example: --spawn-aider=2 or --spawn-aider=1:sonnet")
	rootCmd.Flags().StringVar(&robotSpawnCursor, "spawn-cursor", "", "Cursor Agent CLI (cursor-agent) agents to spawn: count[:model]. Use with --robot-spawn. Example: --spawn-cursor=2")
	rootCmd.Flags().StringVar(&robotSpawnWindsurf, "spawn-windsurf", "", "Windsurf agents to spawn: count[:model] (refused as IDE_ONLY_HARNESS unless [agents] windsurf is overridden to a terminal command). Use with --robot-spawn")
	rootCmd.Flags().StringVar(&robotSpawnPreset, "spawn-preset", "", "Use recipe preset instead of counts. See --robot-recipes. Example: --spawn-preset=standard")
	rootCmd.Flags().BoolVar(&robotSpawnNoUser, "spawn-no-user", false, "Skip user pane creation. Optional with --robot-spawn. For headless/automation")
	rootCmd.Flags().BoolVar(&robotSpawnWait, "spawn-wait", false, "Wait for agents to show ready state before returning. Recommended for automation")
	rootCmd.Flags().StringVar(&robotSpawnTimeout, "spawn-timeout", "30s", "Max wait for agent ready state (e.g., 30s, 1m). Use with --spawn-wait")
	rootCmd.Flags().StringVar(&robotSpawnTimeout, "ready-timeout", "30s", "DEPRECATED alias; use --timeout. Max wait for agent ready state. Use with --spawn-wait")
	rootCmd.Flags().BoolVar(&robotSpawnSafety, "spawn-safety", false, "Fail if session already exists. Prevents accidental reuse of existing sessions")
	rootCmd.Flags().StringVar(&robotSpawnDir, "spawn-dir", "", "Working directory for spawned session. Use with --robot-spawn. Example: --spawn-dir=/path/to/project")
	rootCmd.Flags().BoolVar(&robotSpawnAssignWork, "spawn-assign-work", false, "Enable orchestrator work assignment: get bv triage, claim beads, send work prompts to agents")
	rootCmd.Flags().StringVar(&robotSpawnStrategy, "spawn-assign-strategy", "top-n", "Work assignment strategy (use with --spawn-assign-work). Values: top-n, diverse, dependency-aware, skill-matched")
	rootCmd.Flags().StringVar(&robotSpawnNames, "spawn-names", "", "Custom agent names (comma-separated). Use with --robot-spawn. Example: --spawn-names=alice,bob,charlie")
	rootCmd.Flags().StringVar(&robotSpawnLabel, "spawn-label", "", "Goal label for multi-session support. Use with --robot-spawn. Creates session PROJECT--LABEL. Example: --spawn-label=frontend")
	rootCmd.Flags().StringVar(&robotSpawnPrompt, "spawn-prompt", "", "Initial prompt delivered to every spawned agent once it is ready (implies --spawn-wait; uses robot send's dispatch path). With --spawn-assign-work it prefixes each work prompt. Use with --robot-spawn. Example: --spawn-prompt='Read AGENTS.md first'")
	rootCmd.Flags().StringVar(&robotSpawnPromptFile, "spawn-prompt-file", "", "Read the --spawn-prompt text from a file, or stdin with '-'. Use with --robot-spawn")
	rootCmd.Flags().StringVar(&robotSpawnStaggerMode, "spawn-stagger-mode", "", "Pace prompt delivery between agents (thundering-herd prevention): none, fixed, or smart (learned rate-limit delay). Default: [spawn] stagger_mode, else none. Use with --robot-spawn")
	rootCmd.Flags().StringVar(&robotSpawnStaggerDelay, "spawn-stagger-delay", "", "Interval between consecutive agents' prompts for --spawn-stagger-mode=fixed, 0-5m (e.g. 20s). Default: [spawn] stagger_delay, else 30s. Use with --robot-spawn")

	// Robot-agent-names flag for querying agent name mappings
	rootCmd.Flags().StringVar(&robotAgentNames, "robot-agent-names", "", "Get agent name mappings for a session (JSON). Names are generated using NATO phonetic alphabet. Example: ntm --robot-agent-names=myproject")

	// Robot-controller-spawn flags for launching controller agent
	rootCmd.Flags().StringVar(&robotControllerSpawn, "robot-controller-spawn", "", "Launch controller agent in session. Required: SESSION. Example: ntm --robot-controller-spawn=proj")
	rootCmd.Flags().StringVar(&robotControllerAgentType, "controller-agent-type", "cc", "Agent type for controller: cc, cod, gmi, agy, cursor, windsurf|ws, aider, or ollama. Use with --robot-controller-spawn")
	rootCmd.Flags().StringVar(&robotControllerPrompt, "controller-prompt", "", "Custom prompt file. Use with --robot-controller-spawn")
	rootCmd.Flags().BoolVar(&robotControllerNoPrompt, "controller-no-prompt", false, "Skip initial prompt. Use with --robot-controller-spawn")

	// Robot-interrupt flags for priority course correction
	rootCmd.Flags().StringVar(&robotInterrupt, "robot-interrupt", "", "Send Ctrl+C to stop agents, optionally send a new task. Required: SESSION. Example: ntm --robot-interrupt=proj --msg='Stop and fix bug'")
	rootCmd.Flags().StringVar(&robotInterruptMsg, "interrupt-msg", "", "New task to send after Ctrl+C. Optional with --robot-interrupt. Agents receive this after stopping")
	rootCmd.Flags().BoolVar(&robotInterruptAll, "interrupt-all", false, "Interrupt all panes including user. Default: agents only. Use with --robot-interrupt")
	rootCmd.Flags().BoolVar(&robotInterruptForce, "interrupt-force", false, "Send Ctrl+C even if agent shows idle/ready. Use for stuck agents")
	rootCmd.Flags().BoolVar(&robotInterruptNoWait, "interrupt-no-wait", false, "Return immediately after Ctrl+C without waiting for ready state")
	rootCmd.Flags().StringVar(&robotInterruptTimeout, "interrupt-timeout", "10s", "Max wait for ready state after interrupt (e.g., 10s, 5000ms). Ignored with --no-wait")

	// Robot-restart-pane flag
	rootCmd.Flags().StringVar(&robotRestartPane, "robot-restart-pane", "", "Restart pane processes with tmux respawn-pane -k. Required: SESSION. Optional: --panes, --type, --all, --dry-run, --restart-bead, --restart-prompt, --restart-model, --restart-agent-args, --restart-env-file. Example: ntm --robot-restart-pane=proj --type=claude")
	rootCmd.Flags().StringVar(&robotRestartPaneBead, "restart-bead", "", "Assign bead to agent after restart. Fetches info via br show --json, sends prompt. Use with --robot-restart-pane. Example: --restart-bead=bd-abc12")
	rootCmd.Flags().StringVar(&robotRestartPanePrompt, "restart-prompt", "", "Custom prompt to send after restart. Overrides --restart-bead template. Use with --robot-restart-pane")
	rootCmd.Flags().StringVar(&robotRestartPaneModel, "restart-model", "", "Relaunch with a model override using the spawn variant grammar (model or model@effort). Use with --robot-restart-pane. Example: --restart-model=gpt-5.6-terra@high")
	rootCmd.Flags().StringVar(&robotRestartPaneArgs, "restart-agent-args", "", "Raw arguments appended to the relaunch command (last-flag-wins). Use with --robot-restart-pane")
	rootCmd.Flags().StringVar(&robotRestartEnvironmentFile, "restart-env-file", "", "Private JSON file supplying omitted launch environment for one explicitly selected agent pane. Use with --robot-restart-pane")
	rootCmd.Flags().StringVar(&robotProbe, "robot-probe", "", "Probe pane responsiveness. Required: SESSION. Example: ntm --robot-probe=proj --panes=1.2,%3")
	rootCmd.Flags().StringVar(&robotPaneAddress, "robot-pane-address", "", "Per-pane addressing cards: canonical selector, stable %pane_id, topology, and a ready-to-paste tmux target (immune to base-index settings). Required: SESSION. Example: ntm --robot-pane-address=proj")
	rootCmd.Flags().StringVar(&robotProbeMethod, "probe-method", "", "Probe method: keystroke_echo, interrupt_test, wake_ping (rate-limit liveness: responsiveness + still_rate_limited + tail sample; agent panes only). Used with --robot-probe")
	rootCmd.Flags().IntVar(&robotProbeTimeout, "probe-timeout", 0, "Probe timeout in ms (100-60000, used with --robot-probe)")
	rootCmd.Flags().BoolVar(&robotProbeAggressive, "probe-aggressive", false, "Fallback to interrupt_test if keystroke_echo fails (used with --robot-probe)")

	// Robot-terse flag for ultra-compact output
	rootCmd.Flags().BoolVar(&robotTerse, "robot-terse", false, "Single-line state: S:session|A:active/total|W:working|I:idle|E:errors|B:beads|M:mail|^:attention|!:alerts. Minimal tokens")

	// Robot-format flag for output serialization format
	rootCmd.Flags().StringVar(&robotFormat, "robot-format", "", "Output format for robot commands: json (default), toon (~39% fewer tokens than JSON, measured by TestTokenCorpus_TOONFloor), or auto. Env: NTM_ROBOT_FORMAT, NTM_OUTPUT_FORMAT, TOON_DEFAULT_FORMAT")
	// Deprecated alias for compatibility with older automation/scripts.
	// Keep the backing variable shared so precedence behavior is unchanged.
	rootCmd.Flags().StringVar(&robotFormat, "robot-output-format", "", "DEPRECATED: alias for --robot-format. Output format for robot commands: json, toon, or auto. Env: NTM_ROBOT_FORMAT, NTM_OUTPUT_FORMAT, TOON_DEFAULT_FORMAT")

	// Robot-markdown flags for token-efficient markdown output
	rootCmd.Flags().BoolVar(&robotMarkdown, "robot-markdown", false, "System state as markdown tables. LLM-friendly, ~84% fewer tokens than the JSON snapshot (measured on the committed corpus by TestTokenCorpus_MarkdownFloor)")
	rootCmd.Flags().BoolVar(&robotMarkdownCompact, "md-compact", false, "Ultra-compact markdown: abbreviations, minimal whitespace. Use with --robot-markdown")
	rootCmd.Flags().StringVar(&robotMarkdownSession, "md-session", "", "Filter to one session. Optional with --robot-markdown. Example: --md-session=myproject")
	rootCmd.Flags().StringVar(&robotMarkdownSections, "md-sections", "", "Include only specific sections: summary,sessions,work,alerts,attention. Example: --md-sections=summary,sessions")
	rootCmd.Flags().IntVar(&robotMarkdownMaxBeads, "md-max-beads", 0, "Max beads per category (0=default). Optional with --robot-markdown")
	rootCmd.Flags().IntVar(&robotMarkdownMaxAlerts, "md-max-alerts", 0, "Max alerts to show (0=default). Optional with --robot-markdown")

	// Robot-save flags for session state persistence
	rootCmd.Flags().StringVar(&robotSave, "robot-save", "", "Save session state for later restore. Required: SESSION. Example: ntm --robot-save=proj --output=backup.json")
	rootCmd.Flags().StringVar(&robotSaveOutput, "save-output", "", "Output file path. Optional with --robot-save. Default: ntm-save-{session}-{timestamp}.json")

	// Robot-restore flags for session state restoration
	rootCmd.Flags().StringVar(&robotRestore, "robot-restore", "", "Restore session from saved state. Required: path to save file. Example: ntm --robot-restore=backup.json")
	rootCmd.Flags().BoolVar(&robotRestoreDry, "restore-dry-run", false, "Preview mode: show what would happen without executing. Use with --robot-restore")
	rootCmd.Flags().BoolVar(&robotDryRun, "dry-run", false, "Preview mode: show what would happen without executing. Use with --robot-send, --robot-interrupt, --robot-spawn, --robot-restore, --robot-restart-pane, --robot-smart-restart, --robot-pipeline-run, and --robot-replay")

	// Robot-cass flags for CASS (Cross-Agent Semantic Search) integration
	rootCmd.Flags().BoolVar(&robotCassStatus, "robot-cass-status", false, "Get CASS health: index status, message counts, freshness (JSON)")
	rootCmd.Flags().StringVar(&robotCassSearch, "robot-cass-search", "", "Search past agent conversations. Required: QUERY. Example: ntm --robot-cass-search='authentication error'")
	rootCmd.Flags().BoolVar(&robotCassInsights, "robot-cass-insights", false, "Get CASS aggregated insights: session counts per agent and workspace since --since (default 7d) (JSON)")
	rootCmd.Flags().StringVar(&robotCassContext, "robot-cass-context", "", "Get relevant past context for a task. Example: ntm --robot-cass-context='how to implement auth'")

	// CASS filters - work with --robot-cass-search and --robot-cass-context
	rootCmd.Flags().StringVar(&cassAgent, "cass-agent", "", "Filter CASS by agent: claude, codex, antigravity, gemini, cursor, etc. Example: --cass-agent=claude")
	rootCmd.Flags().StringVar(&cassWorkspace, "cass-workspace", "", "Filter CASS by workspace/project path. Example: --cass-workspace=/path/to/project")
	rootCmd.Flags().StringVar(&cassSince, "cass-since", "", "Filter CASS by recency: 1d, 7d, 30d, etc. Example: --cass-since=7d")
	rootCmd.Flags().IntVar(&cassLimit, "cass-limit", 10, "Max CASS results to return. Example: --cass-limit=20")

	// Robot-jfp flags for JeffreysPrompts (jfp) integration
	rootCmd.Flags().BoolVar(&robotJFPStatus, "robot-jfp-status", false, "Get JFP health: installation status, registry connectivity (JSON)")
	rootCmd.Flags().BoolVar(&robotJFPList, "robot-jfp-list", false, "List all prompts from JeffreysPrompts registry (JSON)")
	rootCmd.Flags().StringVar(&robotJFPSearch, "robot-jfp-search", "", "Search prompts. Required: QUERY. Example: ntm --robot-jfp-search='debugging'")
	rootCmd.Flags().StringVar(&robotJFPShow, "robot-jfp-show", "", "Show prompt details. Required: ID. Example: ntm --robot-jfp-show='prompt-123'")
	rootCmd.Flags().StringVar(&robotJFPSuggest, "robot-jfp-suggest", "", "Get prompt suggestions for a task. Required: TASK. Example: ntm --robot-jfp-suggest='build a REST API'")
	rootCmd.Flags().StringVar(&robotJFPExport, "robot-jfp-export", "", "Export JFP prompt(s). Required: ID(s). Example: ntm --robot-jfp-export='prompt-123'")
	rootCmd.Flags().BoolVar(&robotJFPUpdate, "robot-jfp-update", false, "Update JFP registry cache (JSON)")
	rootCmd.Flags().BoolVar(&robotJFPCategories, "robot-jfp-categories", false, "List all prompt categories with counts (JSON)")
	rootCmd.Flags().BoolVar(&robotJFPTags, "robot-jfp-tags", false, "List all prompt tags with counts (JSON)")
	rootCmd.Flags().BoolVar(&robotJFPBundles, "robot-jfp-bundles", false, "List all prompt bundles (JSON)")

	// JFP filters - work with --robot-jfp-list
	rootCmd.Flags().StringVar(&jfpCategory, "jfp-category", "", "Filter JFP list by category. Example: --jfp-category=coding")
	rootCmd.Flags().StringVar(&jfpTag, "jfp-tag", "", "Filter JFP list by tag. Example: --jfp-tag=debugging")
	rootCmd.Flags().StringVar(&jfpFormat, "jfp-format", "", "Export format for JFP export (skill or md)")

	// MS (Meta Skill) robot flags
	rootCmd.Flags().StringVar(&robotMSSearch, "robot-ms-search", "", "Search Meta Skill catalog. Required: QUERY. Example: ntm --robot-ms-search='commit workflow'")
	rootCmd.Flags().StringVar(&robotMSShow, "robot-ms-show", "", "Show Meta Skill details. Required: ID. Example: ntm --robot-ms-show='commit-and-release'")

	// XF (X Find) robot flags for archive search
	rootCmd.Flags().StringVar(&robotXFSearch, "robot-xf-search", "", "Search X/Twitter archive via xf. Required: QUERY. Example: ntm --robot-xf-search='error handling patterns'")
	rootCmd.Flags().BoolVar(&robotXFStatus, "robot-xf-status", false, "Get XF health: installation status, index validity (JSON)")
	rootCmd.Flags().IntVar(&xfLimit, "xf-limit", 20, "Max XF search results. Optional with --robot-xf-search. Example: --xf-limit=50")
	rootCmd.Flags().StringVar(&xfMode, "xf-mode", "", "XF search mode: lexical, semantic, hybrid (xf default), two-tier. Optional with --robot-xf-search")
	rootCmd.Flags().StringVar(&xfSort, "xf-sort", "", "XF sort order: relevance (xf default), date, date-desc, engagement. Optional with --robot-xf-search")

	// Default prompts robot flag (bd-2ywo)
	rootCmd.Flags().BoolVar(&robotDefaultPrompts, "robot-default-prompts", false, "Get per-agent-type default prompts from config (JSON)")

	// Session profile robot flags (bd-29kr)
	rootCmd.Flags().BoolVar(&robotProfileList, "robot-profile-list", false, "List saved session profiles (JSON)")
	rootCmd.Flags().StringVar(&robotProfileShow, "robot-profile-show", "", "Show a saved session profile by name (JSON). Example: ntm --robot-profile-show=myproject")

	// Robot-tokens flags for token usage analysis
	rootCmd.Flags().BoolVar(&robotTokens, "robot-tokens", false, "Get token usage statistics (JSON). Group by agent, model, or time period")
	rootCmd.Flags().IntVar(&robotTokensDays, "tokens-days", 30, "Days to analyze. Optional with --robot-tokens. Example: --tokens-days=7")
	rootCmd.Flags().StringVar(&robotTokensSince, "tokens-since", "", "Analyze since date (ISO8601 or YYYY-MM-DD). Optional with --robot-tokens")
	rootCmd.Flags().StringVar(&robotTokensGroupBy, "tokens-group-by", "agent", "Grouping: agent, model, day, week, month. Optional with --robot-tokens")
	rootCmd.Flags().StringVar(&robotTokensSession, "tokens-session", "", "Filter to session. Optional with --robot-tokens. Example: --tokens-session=myproject")
	rootCmd.Flags().StringVar(&robotTokensAgent, "tokens-agent", "", "Filter to agent type. Optional with --robot-tokens. Example: --tokens-agent=claude")

	// Robot-history flags for command history tracking
	rootCmd.Flags().StringVar(&robotHistory, "robot-history", "", "Get command history for a session (JSON). Required: SESSION. Example: ntm --robot-history=myproject")
	rootCmd.Flags().StringVar(&robotHistoryPane, "history-pane", "", "Filter by pane ID. Optional with --robot-history. Example: --history-pane=0.1")
	rootCmd.Flags().StringVar(&robotHistoryType, "history-type", "", "Filter by agent type. Optional with --robot-history. Example: --history-type=claude")
	rootCmd.Flags().IntVar(&robotHistoryLast, "history-last", 0, "Show last N entries. Optional with --robot-history. Example: --history-last=10")
	rootCmd.Flags().StringVar(&robotHistorySince, "history-since", "", "Show entries since time (1h, 30m, 2d, or RFC3339). Optional with --robot-history")
	rootCmd.Flags().BoolVar(&robotHistoryStats, "history-stats", false, "Show statistics instead of entries. Optional with --robot-history")

	// Robot-causality flags for unified causality timelines across coordination sources
	rootCmd.Flags().StringVar(&robotCausality, "robot-causality", "", "Get unified causality timeline for a session (JSON). Required: SESSION. Example: ntm --robot-causality=myproject")
	rootCmd.Flags().StringVar(&robotCausalityProject, "causality-project", "", "Project path override for Agent Mail and pipeline state lookup. Optional with --robot-causality")
	rootCmd.Flags().StringVar(&robotCausalityAgent, "causality-agent", "", "Agent Mail identity used to fetch inbox messages. Optional with --robot-causality")
	rootCmd.Flags().StringVar(&robotCausalityBead, "causality-bead", "", "Filter events by bead/thread ID (e.g. bd-2mb03.4). Optional with --robot-causality")
	rootCmd.Flags().StringVar(&robotCausalityPane, "causality-pane", "", "Filter events by pane index/ID. Optional with --robot-causality")
	rootCmd.Flags().StringVar(&robotCausalityType, "causality-type", "", "Filter events by normalized type. Optional with --robot-causality")
	rootCmd.Flags().StringVar(&robotCausalityChain, "causality-chain", "", "Filter events by correlation/chain ID. Optional with --robot-causality")
	rootCmd.Flags().StringVar(&robotCausalitySince, "causality-since", "", "Lower time bound (duration or RFC3339). Optional with --robot-causality")
	rootCmd.Flags().StringVar(&robotCausalityUntil, "causality-until", "", "Upper time bound (duration or RFC3339). Optional with --robot-causality")
	rootCmd.Flags().IntVar(&robotCausalityLimit, "causality-limit", 200, "Max causality events to return (default 200). Optional with --robot-causality")

	// Robot-activity flags for agent activity detection
	rootCmd.Flags().StringVar(&robotActivity, "robot-activity", "", "Get agent activity state (idle/busy/error). Required: SESSION. Example: ntm --robot-activity=myproject")
	rootCmd.Flags().StringVar(&robotActivityType, "activity-type", "", "Filter by agent type: claude, codex, antigravity, grok, omp, gemini. Optional with --robot-activity. Example: --activity-type=omp")
	rootCmd.Flags().StringVar(&robotProductivity, "robot-productivity", "", "Get evidence-backed swarm productivity. Required: SESSION. Example: ntm --robot-productivity=myproject")
	rootCmd.Flags().StringVar(&robotProductivityWindow, "productivity-window", "30m", "Observation window for --robot-productivity and --wait-until=converged. Example: --productivity-window=1h")
	rootCmd.Flags().IntVar(&robotConvergedStreak, "converged-streak", 3, "Consecutive converged observations required by --wait-until=converged")

	// Robot-wait flags for waiting on agent states
	rootCmd.Flags().StringVar(&robotWait, "robot-wait", "", "Wait for agents to reach state. Required: SESSION. Example: ntm --robot-wait=myproject --wait-until=idle")
	rootCmd.Flags().StringVar(&robotWaitUntil, "wait-until", "idle", "Wait condition: idle, complete, generating, healthy, stalled, rate_limited (fires when a pane BECOMES limited), rate_limit_lifted (returns once all target panes are clear), agent_ready (CLI booted and responsive after relaunch/respawn), converged (requires evidence-backed streak), attention, action_required, mail_pending, mail_ack_required, context_hot, reservation_conflict, file_conflict, session_changed, pane_changed. Optional with --robot-wait. Use --attention-cursor and --profile for attention-feed waits.")
	rootCmd.Flags().StringVar(&robotWaitUntil, "condition", "idle", "Alias for --wait-until. Same condition set, including attention-feed waits.")
	rootCmd.Flags().StringVar(&robotWaitTimeout, "wait-timeout", "5m", "Maximum wait time. Optional with --robot-wait. Example: --wait-timeout=2m")
	rootCmd.Flags().StringVar(&robotWaitPoll, "wait-poll", "2s", "Polling interval. Optional with --robot-wait. Example: --wait-poll=500ms")
	rootCmd.Flags().StringVar(&robotWaitPanes, "wait-panes", "", "Comma-separated N, W.P, or %N pane selectors. Optional with --robot-wait. Example: --wait-panes=1,1.0,%7")
	rootCmd.Flags().StringVar(&robotWaitType, "wait-type", "", "Deprecated alias for --type. Filter by agent type with --robot-wait. Example: --type=claude")
	rootCmd.Flags().BoolVar(&robotWaitAny, "wait-any", false, "Wait for ANY agent instead of ALL. Optional with --robot-wait")
	rootCmd.Flags().BoolVar(&robotWaitOnError, "wait-exit-on-error", false, "Exit immediately if ERROR state detected. Optional with --robot-wait")
	rootCmd.Flags().BoolVar(&robotWaitOnError, "exit-on-error", false, "Alias for --wait-exit-on-error. Exit immediately if ERROR state detected")
	rootCmd.Flags().BoolVar(&robotWaitTransition, "wait-transition", false, "Require state transition: agents must leave then return to target state. Use after sending prompts to wait for complete processing cycle. Optional with --robot-wait")
	rootCmd.Flags().BoolVar(&robotWaitTransition, "transition", false, "Alias for --wait-transition")
	rootCmd.Flags().StringVar(&robotWaitID, "wait-id", "", "Durable handle for this wait. Cancel it with --robot-wait-cancel=WAIT_ID. Optional with --robot-wait")
	rootCmd.Flags().StringVar(&robotWaitCancel, "robot-wait-cancel", "", "Cancel one active --robot-wait by its --wait-id")

	// Robot-route flags for routing recommendations
	rootCmd.Flags().StringVar(&robotRoute, "robot-route", "", "Get routing recommendation. Required: SESSION. Example: ntm --robot-route=myproject --strategy=least-loaded")
	rootCmd.Flags().StringVar(&robotRouteStrategy, "route-strategy", "least-loaded", "Routing strategy: least-loaded, first-available, round-robin, round-robin-available, random, sticky, explicit, affinity (needs --msg). Optional with --robot-route")
	rootCmd.Flags().StringVar(&robotRouteLastAgent, "last-agent", "", "Pane ID (%N) of the previously routed agent; anchors sticky and round-robin rotation across stateless CLI invocations. Optional with --robot-route")
	rootCmd.Flags().StringVar(&robotRouteType, "route-type", "", "Deprecated alias for --type. Filter by agent type with --robot-route. Example: --type=claude")
	rootCmd.Flags().StringVar(&robotRouteExclude, "route-exclude", "", "Exclude pane indices (comma-separated). Optional with --robot-route. Example: --route-exclude=0,3")

	// Robot-sequence flags for durable per-pane prompt progress.
	rootCmd.Flags().StringVar(&robotSequence, "robot-sequence", "", "Manage a durable per-pane prompt sequence. Required: NAME. Example: ntm --robot-sequence=review --sequence-action=create --sequence-steps='[\"inspect\",\"challenge\"]'")
	rootCmd.Flags().StringVar(&robotSequenceAction, "sequence-action", "next", "Sequence action: create, next, or advance. Optional with --robot-sequence")
	rootCmd.Flags().StringVar(&robotSequencePane, "sequence-pane", "", "Stable pane ID or canonical address whose prompt position to read or advance. Required for --sequence-action=next|advance")
	rootCmd.Flags().StringVar(&robotSequenceSteps, "sequence-steps", "", "JSON array of ordered prompts. Required for --sequence-action=create")

	// Robot-pipeline flags for workflow execution
	rootCmd.Flags().StringVar(&robotPipelineRun, "robot-pipeline-run", "", "Run a workflow. Required: WORKFLOW_FILE, --session. Example: ntm --robot-pipeline-run=workflow.yaml --session=proj")
	rootCmd.Flags().StringVar(&robotPipelineStatus, "robot-pipeline", "", "Get pipeline status. Required: RUN_ID. Example: ntm --robot-pipeline=run-20241230-123456-abcd")
	rootCmd.Flags().BoolVar(&robotPipelineList, "robot-pipeline-list", false, "List all tracked pipelines. Example: ntm --robot-pipeline-list")
	rootCmd.Flags().StringVar(&robotPipelineCancel, "robot-pipeline-cancel", "", "Cancel a running pipeline. Required: RUN_ID. Example: ntm --robot-pipeline-cancel=run-20241230-123456-abcd")
	rootCmd.Flags().StringVar(&robotPipelineSession, "pipeline-session", "", "Deprecated alias for --session. Tmux session for pipeline execution with --robot-pipeline-run. Example: --session=myproject")
	rootCmd.Flags().StringVar(&robotPipelineVars, "pipeline-vars", "", "JSON variables for pipeline. Optional with --robot-pipeline-run. Example: --pipeline-vars='{\"env\":\"prod\"}'")
	rootCmd.Flags().BoolVar(&robotPipelineDryRun, "pipeline-dry-run", false, "Validate workflow without executing. Optional with --robot-pipeline-run")
	rootCmd.Flags().BoolVar(&robotPipelineBG, "pipeline-background", false, "Run pipeline in background. Optional with --robot-pipeline-run")
	rootCmd.Flags().StringVar(&robotPipelineStartFrom, "pipeline-start-from", "", "Top-level step ID to start from. Optional with --robot-pipeline-run. Example: --pipeline-start-from=step-3")
	rootCmd.Flags().StringVar(&robotPipelineFromState, "pipeline-from-state", "", "Prior run ID whose persisted outputs should be reused for steps skipped by --pipeline-start-from. Requires --pipeline-start-from.")

	// TUI Parity robot flags - expose TUI dashboard functionality to AI agents
	rootCmd.Flags().StringVar(&robotFiles, "robot-files", "", "Get file changes with agent attribution. Optional SESSION filter. Example: ntm --robot-files=myproject --window=15m")
	rootCmd.Flags().Lookup("robot-files").NoOptDefVal = "__present__"
	rootCmd.Flags().StringVar(&robotFilesWindow, "files-window", "15m", "Time window: 5m, 15m, 1h, all. Optional with --robot-files. Example: --files-window=1h")
	rootCmd.Flags().IntVar(&robotFilesLimit, "files-limit", 100, "Max changes to return. Optional with --robot-files. Example: --files-limit=50")

	rootCmd.Flags().StringVar(&robotInspectPane, "robot-inspect-pane", "", "Detailed pane inspection. Required: SESSION. Example: ntm --robot-inspect-pane=myproject --index=1")
	rootCmd.Flags().IntVar(&robotInspectIndex, "inspect-index", 0, "Pane index to inspect. Optional with --robot-inspect-pane. Example: --inspect-index=2")
	rootCmd.Flags().IntVar(&robotInspectLines, "inspect-lines", 100, "Lines to capture. Optional with --robot-inspect-pane. Example: --inspect-lines=200")
	rootCmd.Flags().BoolVar(&robotInspectCode, "inspect-code", false, "Parse code blocks from output. Optional with --robot-inspect-pane")
	rootCmd.Flags().StringVar(&robotInspectSession, "robot-inspect-session", "", "Projection-backed session drill-down. Required: SESSION. Example: ntm --robot-inspect-session=myproject")
	rootCmd.Flags().StringVar(&robotInspectAgent, "robot-inspect-agent", "", "Projection-backed agent drill-down. Required: SESSION:PANE runtime agent id. Example: ntm --robot-inspect-agent=myproject:%1")
	rootCmd.Flags().StringVar(&robotInspectWork, "robot-inspect-work", "", "Projection-backed work drill-down. Required: BEAD_ID. Example: ntm --robot-inspect-work=bd-j9jo3.6.6")
	rootCmd.Flags().StringVar(&robotInspectCoord, "robot-inspect-coordination", "", "Projection-backed coordination drill-down. Required: AGENT_NAME. Example: ntm --robot-inspect-coordination=BlueLake")
	rootCmd.Flags().StringVar(&robotInspectQuota, "robot-inspect-quota", "", "Projection-backed quota drill-down. Required: PROVIDER/ACCOUNT. Canonical aliases like claude/default are accepted. Example: ntm --robot-inspect-quota=claude/default")
	rootCmd.Flags().StringVar(&robotInspectIncident, "robot-inspect-incident", "", "Store-backed incident drill-down. Required: INCIDENT_ID. Example: ntm --robot-inspect-incident=inc_20260323_abc123")
	rootCmd.Flags().StringVar(&robotIncidentResolve, "robot-incident-resolve", "", "Mark a store-backed incident resolved. Required: INCIDENT_ID. Example: ntm --robot-incident-resolve=inc_20260323_abc123")
	rootCmd.Flags().StringVar(&robotExitCLI, "robot-exit-cli", "", "Gracefully exit agent CLIs (double Ctrl+C choreography) without destroying panes. Required: SESSION. Optional: --panes, --relaunch, --respawn-shell, --allow-pane-close. Example: ntm --robot-exit-cli=myproject --panes=1")
	rootCmd.Flags().StringVar(&robotKillAgent, "robot-kill-agent", "", "SIGTERM/SIGKILL agent process trees while preserving panes and shells. Required: SESSION. Optional: --panes, --relaunch. Example: ntm --robot-kill-agent=myproject --panes=2")
	rootCmd.Flags().BoolVar(&robotLifecycleRelaunch, "relaunch", false, "Relaunch the agent CLI after --robot-exit-cli/--robot-kill-agent and verify boot")
	rootCmd.Flags().BoolVar(&robotExitCLIRespawnShell, "respawn-shell", false, "Replace an exited direct-exec agent root process with a fresh interactive shell in the same working directory (--robot-exit-cli)")
	rootCmd.Flags().BoolVar(&robotExitCLIAllowPaneClose, "allow-pane-close", false, "Permit --robot-exit-cli on a direct-exec agent pane to close the pane cleanly after exit")
	rootCmd.Flags().StringVar(&robotKillPane, "robot-kill-pane", "", "Remove specific panes; session and sibling panes survive. Required: SESSION and --panes. Example: ntm --robot-kill-pane=myproject --panes=2")
	rootCmd.Flags().StringVar(&robotDialogs, "robot-dialogs", "", "Classify in-pane interactive dialogs (trust prompt, rate-limit options, usage overlay, paste limbo, destructive confirm) with extracted options. Required: SESSION. Optional: --panes. Example: ntm --robot-dialogs=myproject")
	rootCmd.Flags().StringVar(&robotAnswerDialog, "robot-answer-dialog", "", "Answer a classified dialog by label with policy gating (accept-side refused on destructive confirms). Required: SESSION, --panes (one pane), --choice. Example: ntm --robot-answer-dialog=myproject --panes=1 --choice=decline")
	rootCmd.Flags().StringVar(&robotDialogChoice, "choice", "", "Dialog answer for --robot-answer-dialog: decline, extra-usage, dismiss, or option-K")
	rootCmd.Flags().StringVar(&robotIncidentNote, "incident-note", "", "Optional resolution note for --robot-incident-resolve")

	rootCmd.Flags().StringVar(&robotMetrics, "robot-metrics", "", "Session metrics export. Optional SESSION. Example: ntm --robot-metrics=myproject --period=24h")
	rootCmd.Flags().Lookup("robot-metrics").NoOptDefVal = "__present__"
	rootCmd.Flags().StringVar(&robotMetricsPeriod, "metrics-period", "24h", "Period bounding prompt, crash and restart counts: 1h, 24h, 7d, all (any duration). Optional with --robot-metrics. Example: --metrics-period=7d")
	rootCmd.Flags().BoolVar(&robotDiskAttribution, "disk-attribution", false, "Include per-pane build-dir disk sizes (bounded du). Optional with --robot-metrics. Example: ntm --robot-metrics --disk-attribution")

	rootCmd.Flags().StringVar(&robotReplay, "robot-replay", "", "Replay command from history. Required: SESSION. Use with --id. Example: ntm --robot-replay=myproject --id=1735830245123-a1b2c3d4")
	rootCmd.Flags().StringVar(&robotReplayID, "replay-id", "", "History entry ID to replay. Required with --robot-replay. Get IDs from --robot-history")
	rootCmd.Flags().BoolVar(&robotReplayDryRun, "replay-dry-run", false, "Preview replay without executing. Optional with --robot-replay")

	rootCmd.Flags().BoolVar(&robotPaletteInfo, "robot-palette", false, "Query palette commands. Example: ntm --robot-palette --category=quick")
	rootCmd.Flags().StringVar(&robotPaletteSession, "palette-session", "", "Filter recents to session. Optional with --robot-palette")
	rootCmd.Flags().StringVar(&robotPaletteCategory, "palette-category", "", "Filter by category. Optional with --robot-palette. Example: --palette-category=code_quality")
	rootCmd.Flags().StringVar(&robotPaletteSearch, "palette-search", "", "Search commands. Optional with --robot-palette. Example: --palette-search=test")

	rootCmd.Flags().StringVar(&robotDismissAlert, "robot-dismiss-alert", "", "Dismiss an alert by ID. Example: ntm --robot-dismiss-alert=alert-abc123")
	rootCmd.Flags().Lookup("robot-dismiss-alert").NoOptDefVal = "__present__"
	rootCmd.Flags().StringVar(&robotDismissSession, "dismiss-session", "", "Scope dismissal to session. Optional with --robot-dismiss-alert")
	rootCmd.Flags().BoolVar(&robotDismissAll, "dismiss-all", false, "Dismiss all matching alerts. Use with --robot-dismiss-alert")

	// Robot-diff flags for comparing agent activity (synthesis)
	rootCmd.Flags().StringVar(&robotDiff, "robot-diff", "", "Compare agent activity and file changes. Required: SESSION. Example: ntm --robot-diff=myproject --since=10m")
	rootCmd.Flags().StringVar(&robotDiffSince, "diff-since", "15m", "Deprecated alias for --since. Duration or RFC3339 timestamp to look back from. Optional with --robot-diff. Default: 15m")

	// Robot-alerts flags for alert listing (TUI parity)
	rootCmd.Flags().BoolVar(&robotAlerts, "robot-alerts", false, "List active alerts with filtering. TUI parity for Alerts panel. Example: ntm --robot-alerts --severity=critical")
	rootCmd.Flags().StringVar(&robotAlertsSeverity, "alerts-severity", "", "Filter by severity: info, warning, error, critical. Optional with --robot-alerts")
	rootCmd.Flags().StringVar(&robotAlertsType, "alerts-type", "", "Filter by alert type. Optional with --robot-alerts")
	rootCmd.Flags().StringVar(&robotAlertsSession, "alerts-session", "", "Filter by session. Optional with --robot-alerts")

	// Robot-beads-list flags for bead listing (TUI parity)
	rootCmd.Flags().BoolVar(&robotBeadsList, "robot-beads-list", false, "List beads with filtering. TUI parity for Beads panel. Example: ntm --robot-beads-list --status=open")
	rootCmd.Flags().StringVar(&robotBeadsStatus, "beads-status", "", "Filter by status: open, in_progress, closed, blocked. Optional with --robot-beads-list")
	rootCmd.Flags().StringVar(&robotBeadsPriority, "beads-priority", "", "Filter by priority: 0-4 or P0-P4. Optional with --robot-beads-list")
	rootCmd.Flags().StringVar(&robotBeadsAssignee, "beads-assignee", "", "Filter by assignee. Optional with --robot-beads-list")
	rootCmd.Flags().StringVar(&robotBeadsType, "beads-type", "", "Filter by type: task, bug, feature, epic, chore. Optional with --robot-beads-list")
	rootCmd.Flags().IntVar(&robotBeadsLimit, "beads-limit", 20, "Max beads to return (default 20). Optional with --robot-beads-list")

	// Robot-bead flags for programmatic bead management
	rootCmd.Flags().StringVar(&robotBeadClaim, "robot-bead-claim", "", "Claim a bead by ID. Example: ntm --robot-bead-claim=ntm-abc123")
	rootCmd.Flags().BoolVar(&robotBeadCreate, "robot-bead-create", false, "Create a new bead. Requires --bead-title. Example: ntm --robot-bead-create --bead-title='Fix bug'")
	rootCmd.Flags().StringVar(&robotBeadShow, "robot-bead-show", "", "Show bead details. Example: ntm --robot-bead-show=ntm-abc123")
	rootCmd.Flags().StringVar(&robotBeadClose, "robot-bead-close", "", "Close a bead by ID. Example: ntm --robot-bead-close=ntm-abc123")
	rootCmd.Flags().StringVar(&beadTitle, "bead-title", "", "Title for new bead. Required with --robot-bead-create")
	rootCmd.Flags().StringVar(&beadType, "bead-type", "task", "Type: task, bug, feature, epic, chore. Optional with --robot-bead-create")
	rootCmd.Flags().IntVar(&beadPriority, "bead-priority", 2, "Priority 0-4 (0=critical, 4=backlog). Optional with --robot-bead-create")
	rootCmd.Flags().StringVar(&beadDescription, "bead-description", "", "Description for new bead. Optional with --robot-bead-create")
	rootCmd.Flags().StringVar(&beadLabels, "bead-labels", "", "Comma-separated labels. Optional with --robot-bead-create. Example: --bead-labels=backend,api")
	rootCmd.Flags().StringVar(&beadDependsOn, "bead-depends-on", "", "Comma-separated dependency bead IDs. Optional with --robot-bead-create")
	rootCmd.Flags().StringVar(&beadAssignee, "bead-assignee", "", "Assignee for claim. Optional with --robot-bead-claim")
	rootCmd.Flags().StringVar(&beadCloseReason, "bead-close-reason", "", "Reason for closing. Optional with --robot-bead-close")

	// Robot-summary flags for session activity summary
	rootCmd.Flags().StringVar(&robotSummary, "robot-summary", "", "Get session activity summary with agent metrics. Required: SESSION. Example: ntm --robot-summary=myproject --since=30m")
	rootCmd.Flags().StringVar(&robotSummarySince, "summary-since", "30m", "Deprecated alias for --since. Duration or RFC3339 timestamp to look back from. Optional with --robot-summary. Default: 30m")

	// Help verbosity flags
	rootCmd.Flags().BoolVar(&helpMinimal, "minimal", false, "Show minimal help with essential commands only (spawn, send, status, kill, help)")
	rootCmd.Flags().BoolVar(&helpFull, "full", false, "Show full help with all commands (default behavior)")

	// Robot-switch-account flags for CAAM account switching
	rootCmd.Flags().StringVar(&robotSwitchAccount, "robot-switch-account", "", "Switch CAAM account for provider. Format: provider or provider:account. Example: ntm --robot-switch-account=claude")

	// Robot-account-status and robot-accounts-list flags for CAAM
	rootCmd.Flags().BoolVar(&robotAccountStatus, "robot-account-status", false, "Show CAAM account status per provider. JSON output. Example: ntm --robot-account-status")
	rootCmd.Flags().StringVar(&robotAccountStatusProvider, "account-status-provider", "", "Filter to specific provider. Optional with --robot-account-status. Example: --account-status-provider=claude")
	rootCmd.Flags().BoolVar(&robotAccountsList, "robot-accounts-list", false, "List all CAAM accounts. JSON output. Example: ntm --robot-accounts-list")
	rootCmd.Flags().StringVar(&robotAccountsListProvider, "accounts-list-provider", "", "Filter to specific provider. Optional with --robot-accounts-list. Example: --accounts-list-provider=claude")

	// Robot-env flag for environment info (bd-18gwh)
	rootCmd.Flags().StringVar(&robotEnv, "robot-env", "", "Environment info for agent operation. Pass session name or 'global'. JSON output. Example: ntm --robot-env=myproject")

	// Robot-dcg-status flag for DCG
	rootCmd.Flags().BoolVar(&robotDCGStatus, "robot-dcg-status", false, "Show DCG status and configuration. JSON output. Example: ntm --robot-dcg-status")
	rootCmd.Flags().BoolVar(&robotDCGCheck, "robot-dcg-check", false, "Preflight a shell command via dcg (no execution). JSON output. Requires --command.")
	rootCmd.Flags().BoolVar(&robotDCGCheck, "robot-guard", false, "DEPRECATED: use --robot-dcg-check")
	rootCmd.Flags().StringVar(&robotDCGCmd, "command", "", "Command to preflight with --robot-dcg-check / --robot-guard (no execution). Example: --command='rm -rf /tmp'")
	rootCmd.Flags().StringVar(&robotDCGCmd, "cmd", "", "DEPRECATED: use --command")
	rootCmd.Flags().StringVar(&robotDCGContext, "context", "", "Context/intent for the command (helps DCG make better decisions). Example: --context='Cleaning build artifacts'")
	rootCmd.Flags().StringVar(&robotDCGCwd, "cwd", "", "Working directory context (defaults to current directory). Example: --cwd=/tmp/scratch")
	rootCmd.Flags().BoolVar(&robotSafetySimulate, "robot-safety-simulate", false, "Simulate a safety policy command plan without execution. JSON output. Use --command and/or repeated --step.")
	rootCmd.Flags().StringArrayVar(&robotSafetySteps, "step", nil, "Command step for --robot-safety-simulate; repeat for multi-step plans")
	rootCmd.Flags().BoolVar(&robotPolicyDecisions, "robot-policy-decisions", false, "List durable policy and operator audit decisions. JSON output. Optional: --policy-entity-type, --policy-entity-id, --limit.")
	rootCmd.Flags().StringVar(&robotPolicyEntityType, "policy-entity-type", "", "Filter --robot-policy-decisions by entity type (e.g. attention_item)")
	rootCmd.Flags().StringVar(&robotPolicyEntityID, "policy-entity-id", "", "Filter --robot-policy-decisions by entity ID")
	rootCmd.Flags().StringVar(&robotUBSScan, "robot-ubs-scan", "", "Run UBS static bug analysis (full directory or incremental --scan-staged, --scan-diff, --scan-file) and emit JSON. Optional: =PATH or --scan-path")
	rootCmd.Flags().Lookup("robot-ubs-scan").NoOptDefVal = "__present__"
	rootCmd.Flags().StringVar(&robotScanPath, "scan-path", "", "Directory path to scan with --robot-ubs-scan (defaults to current directory)")
	rootCmd.Flags().StringVar(&robotScanFile, "scan-file", "", "Single file path to scan incrementally with --robot-ubs-scan")
	rootCmd.Flags().BoolVar(&robotScanStaged, "scan-staged", false, "Scan only git-staged files with --robot-ubs-scan")
	rootCmd.Flags().BoolVar(&robotScanDiff, "scan-diff", false, "Scan only modified working-tree files with --robot-ubs-scan")
	rootCmd.Flags().StringSliceVar(&robotScanOnly, "scan-only", nil, "Restrict --robot-ubs-scan to specific languages (e.g. go,ts)")
	rootCmd.Flags().StringSliceVar(&robotScanExclude, "scan-exclude", nil, "Exclude languages from --robot-ubs-scan")
	rootCmd.Flags().BoolVar(&robotScanFailOnWarning, "scan-fail-on-warning", false, "Mark summary.healthy=false on warnings in --robot-ubs-scan")
	rootCmd.Flags().DurationVar(&robotScanTimeout, "scan-timeout", 60*time.Second, "Timeout for --robot-ubs-scan execution")

	// Robot-slb flags for SLB approvals
	rootCmd.Flags().BoolVar(&robotSLBPending, "robot-slb-pending", false, "List pending SLB approval requests. JSON output. Example: ntm --robot-slb-pending")
	rootCmd.Flags().StringVar(&robotSLBApprove, "robot-slb-approve", "", "Approve SLB request by ID. JSON output. Example: ntm --robot-slb-approve=req-123")
	rootCmd.Flags().StringVar(&robotSLBDeny, "robot-slb-deny", "", "Deny SLB request by ID. JSON output. Example: ntm --robot-slb-deny=req-123 --reason='Too risky'")
	rootCmd.Flags().StringVar(&slbReason, "reason", "", "Reason for SLB denial. Required with --robot-slb-deny")

	// Robot-ru-sync flag for RU
	rootCmd.Flags().BoolVar(&robotRUSync, "robot-ru-sync", false, "Run ru sync with JSON output. Optional with --dry-run. Example: ntm --robot-ru-sync")

	// Robot-giil-fetch flag for GIIL
	rootCmd.Flags().StringVar(&robotGiilFetch, "robot-giil-fetch", "", "Download image from share URL via giil (JSON). Required: URL. Example: ntm --robot-giil-fetch=https://share.icloud.com/photos/abc123")

	// Robot-quota-status and robot-quota-check flags for caut
	rootCmd.Flags().BoolVar(&robotQuotaStatus, "robot-quota-status", false, "Show caut quota status for all providers. JSON output. Example: ntm --robot-quota-status")
	rootCmd.Flags().BoolVar(&robotQuotaCheck, "robot-quota-check", false, "Check quota for specific provider. JSON output. Example: ntm --robot-quota-check --provider=claude")
	rootCmd.Flags().StringVar(&robotQuotaCheckProvider, "quota-check-provider", "", "Provider for quota check. Required with --robot-quota-check. Example: --quota-check-provider=claude")

	// Robot-rano-stats flag for per-agent network stats
	rootCmd.Flags().BoolVar(&robotRanoStats, "robot-rano-stats", false, "Get per-agent network stats via rano. JSON output. Example: ntm --robot-rano-stats")
	rootCmd.Flags().StringVar(&robotRanoWindow, "rano-window", "5m", "Time window for --robot-rano-stats (e.g., 5m, 1h)")

	// Robot-rch-status and robot-rch-workers flags for RCH
	rootCmd.Flags().BoolVar(&robotRCHStatus, "robot-rch-status", false, "Get RCH status summary (JSON). Example: ntm --robot-rch-status")
	rootCmd.Flags().BoolVar(&robotProxyStatus, "robot-proxy-status", false, "Get rust_proxy daemon + route status (JSON). Example: ntm --robot-proxy-status")
	rootCmd.Flags().BoolVar(&robotRCHWorkers, "robot-rch-workers", false, "List RCH workers (JSON). Example: ntm --robot-rch-workers")
	rootCmd.Flags().StringVar(&robotRCHWorker, "worker", "", "Filter to a specific RCH worker by name. Optional with --robot-rch-workers")

	// Robot-context-inject flags for context file injection (bd-972v)
	rootCmd.Flags().StringVar(&robotContextInject, "robot-context-inject", "", "Inject project context files (AGENTS.md, README.md) into agent panes. Required: SESSION. Example: ntm --robot-context-inject=myproject")
	rootCmd.Flags().StringVar(&robotContextInjectFiles, "inject-files", "", "Comma-separated files to inject. Optional with --robot-context-inject. Example: --inject-files=AGENTS.md,README.md")
	rootCmd.Flags().IntVar(&robotContextInjectMax, "inject-max-bytes", 0, "Max content size in bytes (0=unlimited). Optional with --robot-context-inject")
	rootCmd.Flags().BoolVar(&robotContextInjectAll, "inject-all", false, "Include user pane. Optional with --robot-context-inject")
	rootCmd.Flags().IntVar(&robotContextInjectPane, "inject-pane", -1, "Specific pane index to inject; rejected if the index exists in multiple windows. Optional with --robot-context-inject")
	rootCmd.Flags().BoolVar(&robotContextInjectDry, "inject-dry-run", false, "Preview without sending. Optional with --robot-context-inject")

	// Robot-mail-check flags for Agent Mail inbox integration (bd-adgv)
	rootCmd.Flags().BoolVar(&robotMailCheck, "robot-mail-check", false, "Check agent inboxes via Agent Mail. Requires --mail-project. JSON output. Example: ntm --robot-mail-check --mail-project=myproject")
	rootCmd.Flags().StringVar(&mailProject, "mail-project", "", "Project for mail check. Required with --robot-mail-check. Example: --mail-project=myproject")
	rootCmd.Flags().StringVar(&mailAgent, "mail-agent", "", "Filter to specific agent inbox. Optional with --robot-mail-check; omit to aggregate project agents. Example: --mail-agent=cc_1")
	rootCmd.Flags().StringVar(&mailThread, "thread", "", "Filter to specific thread. Optional with --robot-mail-check. Example: --thread=TKT-api-design")
	rootCmd.Flags().StringVar(&mailStatus, "mail-status", "", "Filter by read status: read, unread, all. Optional with --robot-mail-check. Example: --mail-status=unread")
	rootCmd.Flags().BoolVar(&mailIncludeBodies, "include-bodies", false, "Include full message bodies. Optional with --robot-mail-check")
	rootCmd.Flags().BoolVar(&mailUrgentOnly, "urgent-only", false, "Only show urgent/high-priority messages. Optional with --robot-mail-check")
	rootCmd.Flags().BoolVar(&mailVerbose, "mail-verbose", false, "Include extra details in output. Optional with --robot-mail-check")
	rootCmd.Flags().IntVar(&mailOffset, "mail-offset", 0, "Skip first N messages for pagination. Optional with --robot-mail-check. Example: --mail-offset=20")
	rootCmd.Flags().StringVar(&mailUntil, "mail-until", "", "Filter to messages before date (YYYY-MM-DD). Optional with --robot-mail-check. Example: --mail-until=2025-12-31")

	// ==========================================================================
	// CANONICAL FLAG ALIASES - Robot Mode API Harmonization
	// ==========================================================================
	// These are the canonical (unprefixed) forms of modifier flags.
	// The prefixed versions above are kept for backward compatibility.
	// See docs/robot-api-design.md for design principles.
	// ==========================================================================

	// Global shared modifiers - these work across multiple commands
	// Note: Some canonical flags (--since, --type, --strategy, --exclude) are already
	// defined above and bound to primary use cases. Those flags serve double-duty.
	// See docs/robot-api-design.md for design principles.
	rootCmd.Flags().IntVar(&cassLimit, "limit", 10, "Max results to return (default varies by command)")
	rootCmd.Flags().IntVar(&robotOffset, "offset", 0, "Pagination offset (default 0)")
	// Note: --since already defined at line 1631 for robotSince (used by --robot-snapshot)
	rootCmd.Flags().StringVar(&cassAgent, "agent", "", "Filter by agent type: claude, codex, antigravity, gemini, cursor, etc.")
	rootCmd.Flags().StringVar(&cassWorkspace, "workspace", "", "Filter by workspace/project path")

	// --category and --tag for JFP
	rootCmd.Flags().StringVar(&jfpCategory, "category", "", "Filter by category")
	rootCmd.Flags().StringVar(&jfpTag, "tag", "", "Filter by tag")
	rootCmd.Flags().StringVar(&robotEnsembleTier, "tier", "", "Filter by tier: core, advanced, experimental, all (used with --robot-ensemble-modes)")

	// --days and --group-by for tokens
	rootCmd.Flags().IntVar(&robotTokensDays, "days", 30, "Number of days to analyze")
	rootCmd.Flags().StringVar(&robotTokensGroupBy, "group-by", "agent", "Grouping: agent, model, day, week, month")

	// --pane and --last, --stats for history
	// Note: --type already defined at line 1689 for robotSendType (used by --robot-send)
	rootCmd.Flags().StringVar(&robotHistoryPane, "pane", "", "Singular N, W.P, or %N pane selector. Optional with --robot-send (exactly one pane) and --robot-history")
	rootCmd.Flags().IntVar(&robotHistoryLast, "last", 0, "Show last N entries")
	rootCmd.Flags().BoolVar(&robotHistoryStats, "stats", false, "Show statistics instead of entries")

	// --timeout, --poll, --any for wait/ack
	rootCmd.Flags().StringVar(&robotWaitTimeout, "timeout", "5m", "Maximum wait/operation timeout. Works with --robot-wait, --robot-ack, --robot-send --track, --robot-interrupt, and --robot-spawn --spawn-wait")
	rootCmd.Flags().StringVar(&robotWaitPoll, "poll", "2s", "Polling interval. Works with --robot-wait, --robot-ack, and --robot-send --track")
	rootCmd.Flags().BoolVar(&robotWaitAny, "any", false, "Match ANY instead of ALL")

	// Note: --strategy already defined at line 1696 for robotAssignStrategy
	// Note: --exclude already defined at line 1690 for robotSendExclude

	// --window for files
	rootCmd.Flags().StringVar(&robotFilesWindow, "window", "15m", "Time window: 5m, 15m, 1h, all")

	// --index, --code for inspect
	rootCmd.Flags().IntVar(&robotInspectIndex, "index", 0, "Pane index to inspect")
	rootCmd.Flags().BoolVar(&robotInspectCode, "code", false, "Parse code blocks from output")

	// --period for metrics
	rootCmd.Flags().StringVar(&robotMetricsPeriod, "period", "24h", "Period bounding --robot-metrics prompt, crash and restart counts: 1h, 24h, 7d, all (any duration)")

	// --severity, --status, --priority, --assignee for alerts/beads
	rootCmd.Flags().StringVar(&robotAlertsSeverity, "severity", "", "Filter by severity: info, warning, error, critical")
	rootCmd.Flags().StringVar(&robotBeadsStatus, "status", "", "Filter by status: open, in_progress, closed, blocked")
	rootCmd.Flags().StringVar(&robotBeadsPriority, "priority", "", "Filter by priority: 0-4 or P0-P4")
	rootCmd.Flags().StringVar(&robotBeadsAssignee, "assignee", "", "Filter by assignee")

	// --threshold for relations
	rootCmd.Flags().Float64Var(&robotRelationsThreshold, "threshold", 0.5, "Correlation threshold (0.0-1.0)")

	// --id for replay
	rootCmd.Flags().StringVar(&robotReplayID, "id", "", "Entry ID for replay")

	// --provider for CAAM/quota
	rootCmd.Flags().StringVar(&robotAccountStatusProvider, "provider", "", "Filter by provider: claude|anthropic, openai, antigravity|agy, gemini|google")

	// --verbose global flag (works with multiple commands)
	rootCmd.Flags().BoolVar(&robotIsWorkingVerbose, "verbose", false, "Include detailed/verbose output for commands that support it, including --robot-is-working, --robot-agent-health, and --robot-smart-restart")

	// --search for palette
	rootCmd.Flags().StringVar(&robotPaletteSearch, "search", "", "Search query for palette commands")

	// --fix, --brief for diagnose
	rootCmd.Flags().BoolVar(&robotDiagnoseFix, "fix", false, "Attempt auto-fix for fixable issues")
	rootCmd.Flags().BoolVar(&robotDiagnoseBrief, "brief", false, "Minimal output (summary only)")

	// --compact, --sections, --max-beads, --max-alerts for markdown
	rootCmd.Flags().BoolVar(&robotMarkdownCompact, "compact", false, "Ultra-compact output format")
	rootCmd.Flags().StringVar(&robotMarkdownSections, "sections", "", "Include only specific sections")
	rootCmd.Flags().IntVar(&robotMarkdownMaxBeads, "max-beads", 0, "Max beads to show")
	rootCmd.Flags().IntVar(&robotMarkdownMaxAlerts, "max-alerts", 0, "Max alerts to show")

	// --skip, --template for bulk-assign
	rootCmd.Flags().StringVar(&robotBulkAssignSkip, "skip", "", "N, W.P, or %N pane selectors to skip (comma-separated)")
	rootCmd.Flags().StringVar(&robotBulkAssignTemplate, "template", "", "Custom prompt template file")

	// --vars, --background for pipeline
	rootCmd.Flags().StringVar(&robotPipelineVars, "vars", "", "JSON variables for pipeline")
	rootCmd.Flags().BoolVar(&robotPipelineBG, "background", false, "Run in background")

	// --session shared flag: canonical replacement for the deprecated
	// prefixed session flags. The deprecation hints below point at
	// --session, so it must actually be registered on this command surface
	// (ntm#214: the hint previously suggested a flag that didn't exist).
	rootCmd.Flags().StringVar(&robotSharedSession, "session", "", "Session name. Canonical form for --robot-pipeline-run, --robot-tokens, --robot-alerts, and --robot-palette (replaces --pipeline-session, --tokens-session, --alerts-session, --palette-session)")

	// --no-wait for interrupt
	rootCmd.Flags().BoolVar(&robotInterruptNoWait, "no-wait", false, "Return immediately without waiting")

	// ==========================================================================
	// DEPRECATION WARNINGS - Mark old prefixed flags as deprecated
	// ==========================================================================
	// These flags still work but will show a deprecation warning.
	// See docs/robot-api-design.md for canonical flag patterns.

	// CASS prefixed flags → canonical forms
	rootCmd.Flags().MarkDeprecated("cass-limit", "use --limit instead")
	rootCmd.Flags().MarkDeprecated("cass-since", "use --since instead")
	rootCmd.Flags().MarkDeprecated("cass-agent", "use --agent instead")
	rootCmd.Flags().MarkDeprecated("cass-workspace", "use --workspace instead")

	// JFP prefixed flags → canonical forms
	rootCmd.Flags().MarkDeprecated("jfp-category", "use --category instead")
	rootCmd.Flags().MarkDeprecated("jfp-tag", "use --tag instead")

	// Tokens prefixed flags → canonical forms
	rootCmd.Flags().MarkDeprecated("tokens-days", "use --days instead")
	rootCmd.Flags().MarkDeprecated("tokens-group-by", "use --group-by instead")
	rootCmd.Flags().MarkDeprecated("tokens-since", "use --since instead")
	rootCmd.Flags().MarkDeprecated("tokens-session", "use --session instead")
	rootCmd.Flags().MarkDeprecated("tokens-agent", "use --agent instead")

	// History prefixed flags → canonical forms
	rootCmd.Flags().MarkDeprecated("history-pane", "use --pane instead")
	rootCmd.Flags().MarkDeprecated("history-type", "use --type instead")
	rootCmd.Flags().MarkDeprecated("history-last", "use --last instead")
	rootCmd.Flags().MarkDeprecated("history-since", "use --since instead")
	rootCmd.Flags().MarkDeprecated("history-stats", "use --stats instead")

	// Wait prefixed flags → canonical forms
	rootCmd.Flags().MarkDeprecated("wait-timeout", "use --timeout instead")
	rootCmd.Flags().MarkDeprecated("wait-poll", "use --poll instead")
	rootCmd.Flags().MarkDeprecated("wait-panes", "use --panes instead")
	rootCmd.Flags().MarkDeprecated("wait-type", "use --type instead")
	rootCmd.Flags().MarkDeprecated("wait-any", "use --any instead")

	// Route prefixed flags → canonical forms
	rootCmd.Flags().MarkDeprecated("route-strategy", "use --strategy instead")
	rootCmd.Flags().MarkDeprecated("route-type", "use --type instead")
	rootCmd.Flags().MarkDeprecated("route-exclude", "use --exclude instead")

	// Files prefixed flags → canonical forms
	rootCmd.Flags().MarkDeprecated("files-window", "use --window instead")
	rootCmd.Flags().MarkDeprecated("files-limit", "use --limit instead")

	// Inspect prefixed flags → canonical forms
	rootCmd.Flags().MarkDeprecated("inspect-index", "use --index instead")
	rootCmd.Flags().MarkDeprecated("inspect-lines", "use --lines instead")
	rootCmd.Flags().MarkDeprecated("inspect-code", "use --code instead")

	// Metrics prefixed flags → canonical forms
	rootCmd.Flags().MarkDeprecated("metrics-period", "use --period instead")

	// Alerts prefixed flags → canonical forms
	rootCmd.Flags().MarkDeprecated("alerts-severity", "use --severity instead")
	rootCmd.Flags().MarkDeprecated("alerts-session", "use --session instead")

	// Beads prefixed flags → canonical forms
	rootCmd.Flags().MarkDeprecated("beads-status", "use --status instead")
	rootCmd.Flags().MarkDeprecated("beads-priority", "use --priority instead")
	rootCmd.Flags().MarkDeprecated("beads-assignee", "use --assignee instead")
	rootCmd.Flags().MarkDeprecated("beads-limit", "use --limit instead")

	// Relations prefixed flags → canonical forms
	rootCmd.Flags().MarkDeprecated("relations-limit", "use --limit instead")
	rootCmd.Flags().MarkDeprecated("relations-threshold", "use --threshold instead")

	// Replay prefixed flags → canonical forms
	rootCmd.Flags().MarkDeprecated("replay-id", "use --id instead")
	rootCmd.Flags().MarkDeprecated("replay-dry-run", "use --dry-run instead")

	// Verbose prefixed flags → canonical --verbose
	rootCmd.Flags().MarkDeprecated("is-working-verbose", "use --verbose instead")
	rootCmd.Flags().MarkDeprecated("agent-health-verbose", "use --verbose instead")
	rootCmd.Flags().MarkDeprecated("smart-restart-verbose", "use --verbose instead")

	// Palette prefixed flags → canonical forms
	rootCmd.Flags().MarkDeprecated("palette-session", "use --session instead")
	rootCmd.Flags().MarkDeprecated("palette-category", "use --category instead")
	rootCmd.Flags().MarkDeprecated("palette-search", "use --search instead")

	// Diagnose prefixed flags → canonical forms
	rootCmd.Flags().MarkDeprecated("diagnose-fix", "use --fix instead")
	rootCmd.Flags().MarkDeprecated("diagnose-brief", "use --brief instead")

	// Markdown prefixed flags → canonical forms
	rootCmd.Flags().MarkDeprecated("md-compact", "use --compact instead")
	rootCmd.Flags().MarkDeprecated("md-session", "use --session instead")
	rootCmd.Flags().MarkDeprecated("md-sections", "use --sections instead")
	rootCmd.Flags().MarkDeprecated("md-max-beads", "use --max-beads instead")
	rootCmd.Flags().MarkDeprecated("md-max-alerts", "use --max-alerts instead")

	// Bulk-assign prefixed flags → canonical forms
	rootCmd.Flags().MarkDeprecated("bulk-strategy", "use --strategy instead")
	rootCmd.Flags().MarkDeprecated("skip-panes", "use --skip instead")
	rootCmd.Flags().MarkDeprecated("prompt-template", "use --template instead")

	// Pipeline prefixed flags → canonical forms
	rootCmd.Flags().MarkDeprecated("pipeline-session", "use --session instead")
	rootCmd.Flags().MarkDeprecated("pipeline-vars", "use --vars instead")
	rootCmd.Flags().MarkDeprecated("pipeline-dry-run", "use --dry-run instead")
	rootCmd.Flags().MarkDeprecated("pipeline-background", "use --background instead")

	// Interrupt prefixed flags → canonical forms
	rootCmd.Flags().MarkDeprecated("interrupt-msg", "use --msg instead")
	rootCmd.Flags().MarkDeprecated("interrupt-all", "use --all instead")
	rootCmd.Flags().MarkDeprecated("interrupt-force", "use --force instead")
	rootCmd.Flags().MarkDeprecated("interrupt-no-wait", "use --no-wait instead")
	rootCmd.Flags().MarkDeprecated("interrupt-timeout", "use --timeout instead")

	// Account/provider prefixed flags → canonical forms
	rootCmd.Flags().MarkDeprecated("account-status-provider", "use --provider instead")
	rootCmd.Flags().MarkDeprecated("accounts-list-provider", "use --provider instead")
	rootCmd.Flags().MarkDeprecated("quota-check-provider", "use --provider instead")

	// Triage prefixed flags → canonical forms
	rootCmd.Flags().MarkDeprecated("triage-limit", "use --limit instead")

	// File-beads prefixed flags → canonical forms
	rootCmd.Flags().MarkDeprecated("file-beads-limit", "use --limit instead")

	// Hotspots prefixed flags → canonical forms
	rootCmd.Flags().MarkDeprecated("hotspots-limit", "use --limit instead")

	// Attention prefixed flags → canonical forms
	rootCmd.Flags().MarkDeprecated("attention-limit", "use --limit instead")

	// Dismiss prefixed flags → canonical forms
	rootCmd.Flags().MarkDeprecated("dismiss-session", "use --session instead")
	rootCmd.Flags().MarkDeprecated("dismiss-all", "use --all instead")

	// Summary prefixed flags → canonical forms
	rootCmd.Flags().MarkDeprecated("summary-since", "use --since instead")

	// Diff prefixed flags → canonical forms
	rootCmd.Flags().MarkDeprecated("diff-since", "use --since instead")

	// Save prefixed flags → canonical forms
	rootCmd.Flags().MarkDeprecated("save-output", "use --output instead")

	// Restore prefixed flags → canonical forms
	rootCmd.Flags().MarkDeprecated("restore-dry-run", "use --dry-run instead")

	// Smart-restart prefixed flags → canonical forms
	rootCmd.Flags().MarkDeprecated("smart-restart-dry-run", "use --dry-run instead")

	// Spawn prefixed flags → canonical forms
	rootCmd.Flags().MarkDeprecated("spawn-timeout", "use --timeout instead")
	rootCmd.Flags().MarkDeprecated("ready-timeout", "use --timeout instead")
	rootCmd.Flags().MarkDeprecated("spawn-assign-strategy", "use --strategy instead")

	// Ack prefixed flags → canonical forms
	rootCmd.Flags().MarkDeprecated("ack-timeout", "use --timeout instead")
	rootCmd.Flags().MarkDeprecated("ack-poll", "use --poll instead")

	// Bead prefixed flags → canonical forms (for filters on bead operations)
	rootCmd.Flags().MarkDeprecated("bead-limit", "use --limit instead")
}

func registerConfiguredRobotPluginSpawnFlags() {
	// Register after every built-in root flag so presets cannot replace a
	// robot control flag such as --spawn-timeout or --spawn-omp.
	loadedAgents, _ := plugins.LoadAgentPlugins(pluginAgentsDirForArgs(os.Args[1:]))
	hasPiPlugin := false
	for _, p := range loadedAgents {
		if strings.EqualFold(strings.TrimSpace(p.Name), "pi") || strings.EqualFold(strings.TrimSpace(p.Alias), "pi") {
			hasPiPlugin = true
			break
		}
	}
	if !hasPiPlugin {
		loadedAgents = append(loadedAgents, plugins.ShippedPiPlugin())
	}
	robotSpawnPlugins = registerRobotPluginSpawnFlags(rootCmd, loadedAgents)
}

func registerRobotPluginSpawnFlags(cmd *cobra.Command, loaded []plugins.AgentPlugin) map[string]*string {
	values := make(map[string]*string)
	for _, plugin := range loaded {
		name := strings.ToLower(strings.TrimSpace(plugin.Name))
		if agent.AgentType(name).IsValid() || name == "unknown" {
			continue
		}
		if _, duplicate := values[name]; duplicate {
			continue
		}
		value := new(string)
		registered := false
		for _, raw := range []string{name, plugin.Alias} {
			alias := strings.ToLower(strings.TrimSpace(raw))
			if alias == "" || agent.AgentType(alias).IsValid() || alias == "unknown" {
				continue
			}
			flag := "spawn-" + alias
			if cmd.Flags().Lookup(flag) != nil {
				continue
			}
			cmd.Flags().StringVar(value, flag, "", "Registered "+name+" agents to spawn: count[:model[:effort]]. Use with --robot-spawn")
			registered = true
		}
		if registered {
			values[name] = value
		}
	}
	return values
}

func resolveRobotSharedFlag(cmd *cobra.Command, specificFlagName string, specificValue string, sharedFlagName string, sharedValue string) string {
	if cmd != nil && specificFlagName != "" && cmd.Flags().Changed(specificFlagName) {
		return specificValue
	}
	if cmd != nil && sharedFlagName != "" && cmd.Flags().Changed(sharedFlagName) {
		return sharedValue
	}
	if strings.TrimSpace(specificValue) != "" {
		return specificValue
	}
	return specificValue
}

func resolveRobotSharedBool(cmd *cobra.Command, specificFlagName string, specificValue bool, sharedFlagName string, sharedValue bool) bool {
	if cmd != nil && specificFlagName != "" && cmd.Flags().Changed(specificFlagName) {
		return specificValue
	}
	if cmd != nil && sharedFlagName != "" && cmd.Flags().Changed(sharedFlagName) {
		return sharedValue
	}
	return specificValue
}

func resolveRobotHistoryType(cmd *cobra.Command) string {
	return resolveRobotSharedFlag(cmd, "history-type", robotHistoryType, "type", robotSendType)
}

func resolveRobotHistorySince(cmd *cobra.Command) string {
	return resolveRobotSharedFlag(cmd, "history-since", robotHistorySince, "since", robotSince)
}

func resolveRobotTokensSince(cmd *cobra.Command) string {
	return resolveRobotSharedFlag(cmd, "tokens-since", robotTokensSince, "since", robotSince)
}

func resolveRobotWaitType(cmd *cobra.Command) string {
	return resolveRobotSharedFlag(cmd, "wait-type", robotWaitType, "type", robotSendType)
}

func resolveRobotWaitPanes(cmd *cobra.Command) string {
	return resolveRobotSharedFlag(cmd, "wait-panes", robotWaitPanes, "panes", robotPanes)
}

func resolveRobotRouteType(cmd *cobra.Command) string {
	return resolveRobotSharedFlag(cmd, "route-type", robotRouteType, "type", robotSendType)
}

func resolveRobotRouteStrategy(cmd *cobra.Command) string {
	return resolveRobotSharedFlag(cmd, "route-strategy", robotRouteStrategy, "strategy", robotAssignStrategy)
}

// robotRouteOnlyModifiers are the --robot-route modifiers that --robot-send
// never reads. Accepting them silently made `--robot-send --strategy=X` look
// like a routed send while it actually delivered to every matching pane.
var robotRouteOnlyModifiers = []string{"strategy", "route-strategy", "last-agent", "route-type", "route-exclude", "relax"}

// changedRobotRouteModifier returns the first --robot-route-only modifier the
// caller set (as "--name"), or "" when none was set.
func changedRobotRouteModifier(cmd *cobra.Command) string {
	for _, name := range robotRouteOnlyModifiers {
		if cmd.Flags().Changed(name) {
			return "--" + name
		}
	}
	return ""
}

func resolveRobotBulkAssignStrategy(cmd *cobra.Command) string {
	return resolveRobotSharedFlag(cmd, "bulk-strategy", robotBulkAssignStrategy, "strategy", robotAssignStrategy)
}

func resolveRobotRouteExclude(cmd *cobra.Command) string {
	return resolveRobotSharedFlag(cmd, "route-exclude", robotRouteExclude, "exclude", robotSendExclude)
}

func resolveRobotDiffSince(cmd *cobra.Command) string {
	return resolveRobotSharedFlag(cmd, "diff-since", robotDiffSince, "since", robotSince)
}

func resolveRobotSummarySince(cmd *cobra.Command) string {
	return resolveRobotSharedFlag(cmd, "summary-since", robotSummarySince, "since", robotSince)
}

func resolveRobotSaveOutput(cmd *cobra.Command) string {
	return resolveRobotSharedFlag(cmd, "save-output", robotSaveOutput, "output", robotMonitorOutput)
}

// resolveRobotCASSSince resolves the time window for the commands that read
// --cass-since (deprecated) or the shared --since: CASS search and insights,
// and mail check.
func resolveRobotCASSSince(cmd *cobra.Command) string {
	return resolveRobotSharedFlag(cmd, "cass-since", cassSince, "since", robotSince)
}

// resolveRobotSharedLimit applies the shared --limit to a command that also
// has its own limit flag: the specific flag wins when set, then --limit, then
// the specific flag's default.
func resolveRobotSharedLimit(cmd *cobra.Command, specificFlagName string, specificValue int) int {
	if cmd != nil && cmd.Flags().Changed(specificFlagName) {
		return specificValue
	}
	if cmd != nil && cmd.Flags().Changed("limit") {
		return cassLimit
	}
	return specificValue
}

// resolveRobotSharedLines is resolveRobotSharedLimit for the shared --lines.
func resolveRobotSharedLines(cmd *cobra.Command, specificFlagName string, specificValue int) int {
	if cmd != nil && cmd.Flags().Changed(specificFlagName) {
		return specificValue
	}
	if cmd != nil && cmd.Flags().Changed("lines") {
		return robotLines
	}
	return specificValue
}

func resolveRobotMailCheckLimit(cmd *cobra.Command) int {
	if cmd != nil && cmd.Flags().Changed("cass-limit") {
		return cassLimit
	}
	if cmd != nil && cmd.Flags().Changed("limit") {
		return cassLimit
	}
	return 0
}

func resolveRobotProvider(cmd *cobra.Command, specificFlagName string, specificValue string) string {
	sharedValue := ""
	if cmd != nil {
		if providerValue, err := cmd.Flags().GetString("provider"); err == nil {
			sharedValue = providerValue
		}
	}
	return resolveRobotSharedFlag(cmd, specificFlagName, specificValue, "provider", sharedValue)
}

func resolveRobotInterruptMessage(cmd *cobra.Command) string {
	return resolveRobotSharedFlag(cmd, "interrupt-msg", robotInterruptMsg, "msg", robotSendMsg)
}

func resolveRobotInterruptAll(cmd *cobra.Command) bool {
	return resolveRobotSharedBool(cmd, "interrupt-all", robotInterruptAll, "all", robotSendAll)
}

func resolveRobotInterruptForce(cmd *cobra.Command) bool {
	return resolveRobotSharedBool(cmd, "interrupt-force", robotInterruptForce, "force", robotSmartRestartForce)
}

func resolveRobotInterruptTimeout(cmd *cobra.Command) string {
	return resolveRobotSharedFlag(cmd, "interrupt-timeout", robotInterruptTimeout, "timeout", robotWaitTimeout)
}

func resolveRobotAgentHealthVerbose(cmd *cobra.Command) bool {
	return resolveRobotSharedBool(cmd, "agent-health-verbose", robotAgentHealthVerbose, "verbose", robotIsWorkingVerbose)
}

func resolveRobotSmartRestartVerbose(cmd *cobra.Command) bool {
	return resolveRobotSharedBool(cmd, "smart-restart-verbose", robotSmartRestartVerbose, "verbose", robotIsWorkingVerbose)
}

func resolveRobotSmartRestartDryRun(cmd *cobra.Command) bool {
	return resolveRobotSharedBool(cmd, "smart-restart-dry-run", robotSmartRestartDryRun, "dry-run", robotDryRun)
}

func resolveRobotPipelineDryRun(cmd *cobra.Command) bool {
	return resolveRobotSharedBool(cmd, "pipeline-dry-run", robotPipelineDryRun, "dry-run", robotDryRun)
}

func resolveRobotPipelineSession(cmd *cobra.Command) string {
	return resolveRobotSharedFlag(cmd, "pipeline-session", robotPipelineSession, "session", robotSharedSession)
}

func resolveRobotTokensSession(cmd *cobra.Command) string {
	return resolveRobotSharedFlag(cmd, "tokens-session", robotTokensSession, "session", robotSharedSession)
}

func resolveRobotAlertsSession(cmd *cobra.Command) string {
	return resolveRobotSharedFlag(cmd, "alerts-session", robotAlertsSession, "session", robotSharedSession)
}

func resolveRobotPaletteSession(cmd *cobra.Command) string {
	return resolveRobotSharedFlag(cmd, "palette-session", robotPaletteSession, "session", robotSharedSession)
}

func resolveRobotReplayDryRun(cmd *cobra.Command) bool {
	return resolveRobotSharedBool(cmd, "replay-dry-run", robotReplayDryRun, "dry-run", robotDryRun)
}

func parseRobotSinceWindow(value string, defaultUnit time.Duration, flagName string) (time.Duration, error) {
	if duration, err := util.ParseDurationWithDefault(value, defaultUnit, flagName); err == nil {
		return duration, nil
	}

	value = strings.TrimSpace(value)
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		if ts, err := time.Parse(layout, value); err == nil {
			now := time.Now().UTC()
			ts = ts.UTC()
			if ts.After(now) {
				return 0, fmt.Errorf("timestamp %q is in the future", value)
			}
			return now.Sub(ts), nil
		}
	}

	return 0, fmt.Errorf("invalid time %q: use duration like 10m or RFC3339 timestamp", value)
}
