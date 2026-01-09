const fs = require("fs");
const path = require("path");

const root = process.cwd();
const parityPath = path.join(root, "docs", "parity_matrix.json");
const openapiPath = path.join(root, "docs", "openapi.json");

const now = new Date().toISOString();

const readJSON = (filePath) => {
  if (!fs.existsSync(filePath)) {
    return null;
  }
  return JSON.parse(fs.readFileSync(filePath, "utf8"));
};

const writeJSON = (filePath, data) => {
  fs.writeFileSync(filePath, JSON.stringify(data, null, 2) + "\n");
};

const extraEndpoints = [
  {
    id: "auth.login",
    method: "post",
    path: "/auth/login",
    tags: ["auth"],
    summary: "Login",
    description: "Exchange credentials for access and refresh tokens.",
    source: null,
    requestSchema: "AuthLoginRequest",
    responseSchema: "AuthLoginResponse",
  },
  {
    id: "auth.refresh",
    method: "post",
    path: "/auth/refresh",
    tags: ["auth"],
    summary: "Refresh access token",
    description: "Issue a new access token using a refresh token.",
    source: null,
    requestSchema: "AuthRefreshRequest",
    responseSchema: "AuthRefreshResponse",
  },
  {
    id: "auth.logout",
    method: "post",
    path: "/auth/logout",
    tags: ["auth"],
    summary: "Logout",
    description: "Invalidate the current refresh token.",
    source: null,
    requestSchema: "AuthLogoutRequest",
    responseSchema: "SuccessResponse",
  },
  {
    id: "auth.whoami",
    method: "get",
    path: "/auth/whoami",
    tags: ["auth"],
    summary: "Current user",
    description: "Return the authenticated user and org context.",
    source: null,
    requestSchema: null,
    responseSchema: "AuthWhoamiResponse",
  },
];

const mergeEndpoints = (base, extra) => {
  const byId = new Map();
  for (const ep of base) {
    byId.set(ep.id, ep);
  }
  for (const ep of extra) {
    byId.set(ep.id, ep);
  }
  return Array.from(byId.values());
};

const addPathParams = (pathPattern) => {
  const params = [];
  const matches = pathPattern.matchAll(/\{([^}]+)\}/g);
  for (const match of matches) {
    const name = match[1];
    params.push({
      name,
      in: "path",
      required: true,
      schema: { type: "string" },
    });
  }
  return params;
};

const exampleMap = (value) => ({
  example: { value },
});

const multiExamples = (entries) => {
  const out = {};
  for (const [name, value] of entries) {
    out[name] = { value };
  }
  return out;
};

const exampleOverrides = {
  "auth.login": {
    request: multiExamples([
      [
        "password",
        {
          provider: "local",
          username: "jordan",
          password: "correct-horse-battery-staple",
          otp: "123456",
          device_name: "macbook-pro-16",
        },
      ],
    ]),
    response: multiExamples([
      [
        "success",
        {
          success: true,
          token_type: "bearer",
          access_token: "ntm_at_01HZZV8R4WJY9W9Y2M6J1T8M0N",
          refresh_token: "ntm_rt_01HZZV8S52S4H7B4J0QH7A7F2M",
          expires_in: 3600,
          user: {
            id: "user_01",
            name: "Jordan Lee",
            email: "jordan@ntm.dev",
            roles: ["admin"],
            org_id: "org_01",
          },
        },
      ],
    ]),
  },
  "auth.refresh": {
    request: exampleMap({
      refresh_token: "ntm_rt_01HZZV8S52S4H7B4J0QH7A7F2M",
    }),
    response: exampleMap({
      success: true,
      token_type: "bearer",
      access_token: "ntm_at_01HZZV9A6Q0DXJ4C2N7KJ6B5M7",
      expires_in: 3600,
    }),
  },
  "auth.logout": {
    request: exampleMap({
      refresh_token: "ntm_rt_01HZZV8S52S4H7B4J0QH7A7F2M",
    }),
    response: exampleMap({
      success: true,
      message: "Logged out.",
      timestamp: "2026-01-07T00:00:00Z",
    }),
  },
  "auth.whoami": {
    response: exampleMap({
      success: true,
      user: {
        id: "user_01",
        name: "Jordan Lee",
        email: "jordan@ntm.dev",
        roles: ["admin"],
        org_id: "org_01",
      },
      org: {
        id: "org_01",
        name: "NTM Labs",
        plan: "enterprise",
      },
    }),
  },
  "core.health": {
    response: exampleMap({
      success: true,
      status: "ok",
      version: "0.1.0-draft",
      uptime_sec: 1834,
      timestamp: "2026-01-07T00:00:00Z",
    }),
  },
  "core.version": {
    response: exampleMap({
      success: true,
      version: "0.1.0-draft",
      commit: "8f4c2d1",
      build_date: "2026-01-07T00:00:00Z",
      go_version: "go1.25.0",
    }),
  },
  "sessions.list": {
    response: multiExamples([
      [
        "active",
        {
          success: true,
          count: 2,
          sessions: [
            {
              id: "sess_proj",
              name: "ntm",
              project_path: "/data/projects/ntm",
              created_at: "2026-01-06T18:10:12Z",
              status: "active",
              pane_count: 4,
              active_pane: 2,
              layout: "tiled",
            },
            {
              id: "sess_docs",
              name: "docs",
              project_path: "/data/projects/ntm-docs",
              created_at: "2026-01-06T20:44:05Z",
              status: "idle",
              pane_count: 2,
              active_pane: 0,
              layout: "even-horizontal",
            },
          ],
        },
      ],
    ]),
  },
  "sessions.get": {
    response: exampleMap({
      success: true,
      session: {
        id: "sess_proj",
        name: "ntm",
        project_path: "/data/projects/ntm",
        created_at: "2026-01-07T00:00:00Z",
        status: "active",
        pane_count: 4,
        active_pane: 1,
        layout: "tiled",
      },
    }),
  },
  "sessions.create": {
    request: multiExamples([
      [
        "with_agents",
        {
          name: "ntm",
          project_dir: "/data/projects/ntm",
          panes: 4,
          layout: "tiled",
          agents: [
            { type: "claude", model: "opus-4.1", pane: 1 },
            { type: "codex", model: "gpt-5-codex", pane: 2 },
          ],
          env: {
            AGENT_MAIL_GUARD_MODE: "warn",
          },
        },
      ],
    ]),
    response: exampleMap({
      success: true,
      session: {
        id: "sess_proj",
        name: "ntm",
        project_path: "/data/projects/ntm",
        created_at: "2026-01-07T00:00:00Z",
        status: "active",
        pane_count: 4,
        active_pane: 1,
        layout: "tiled",
      },
      pane_count: 4,
    }),
  },
  "sessions.spawn": {
    request: exampleMap({
      agent_type: "claude",
      model: "opus-4.1",
      count: 2,
      panes: [1, 2],
      prompt: "Investigate the robot-mode pipeline.",
      env: {
        AGENT_NAME: "BlueLake",
      },
    }),
    response: exampleMap({
      success: true,
      spawned: 2,
      agents: [
        { name: "BlueLake", pane: 1, type: "claude", model: "opus-4.1" },
        { name: "GreenCastle", pane: 2, type: "claude", model: "opus-4.1" },
      ],
    }),
  },
  "agents.send": {
    request: multiExamples([
      [
        "command",
        {
          pane: 1,
          text: "rg --files internal",
          enter: true,
        },
      ],
      [
        "multiline",
        {
          pane: 2,
          text: "go test ./...",
          enter: true,
          raw: false,
        },
      ],
    ]),
    response: exampleMap({
      success: true,
      pane: 1,
      bytes: 21,
      echoed: true,
      timestamp: "2026-01-07T00:00:00Z",
    }),
  },
  "sessions.interrupt": {
    request: exampleMap({
      pane: 1,
      signal: "SIGINT",
      reason: "Stop long-running grep",
    }),
    response: exampleMap({
      success: true,
      pane: 1,
      signal: "SIGINT",
    }),
  },
  "agents.list": {
    response: exampleMap({
      success: true,
      agents: [
        {
          type: "claude",
          display_name: "Claude",
          models: ["opus-4.1", "sonnet-4.1"],
          capabilities: ["code", "analysis", "tools"],
        },
        {
          type: "codex",
          display_name: "OpenAI Codex",
          models: ["gpt-5-codex"],
          capabilities: ["code", "review", "tools"],
        },
        {
          type: "gemini",
          display_name: "Gemini CLI",
          models: ["gemini-2.0"],
          capabilities: ["code", "analysis"],
        },
      ],
    }),
  },
  "robot.status": {
    response: exampleMap({
      success: true,
      timestamp: "2026-01-07T00:00:00Z",
      sessions: [
        {
          id: "sess_proj",
          name: "ntm",
          panes: 4,
          status: "active",
        },
      ],
      panes: [
        { session_id: "sess_proj", pane: 0, title: "shell", busy: false },
        { session_id: "sess_proj", pane: 1, title: "claude", busy: true },
      ],
      agents: [
        {
          name: "BlueLake",
          type: "claude",
          pane: 1,
          status: "running",
          last_active_at: "2026-01-07T00:00:00Z",
        },
      ],
      alerts: [],
      notes: ["Robot mode enabled"],
    }),
  },
  "robot.send": {
    request: exampleMap({
      session: "ntm",
      panes: [1],
      msg: "Summarize the open API changes.",
      type: "claude",
      track: true,
    }),
    response: exampleMap({
      success: true,
      timestamp: "2026-01-07T00:00:00Z",
      targets: ["1"],
    }),
  },
  "robot.ack": {
    request: exampleMap({
      session: "ntm",
      panes: [1],
      msg: "ACK",
      ack_timeout: "30s",
    }),
    response: exampleMap({
      success: true,
      timestamp: "2026-01-07T00:00:30Z",
      responses: [
        {
          pane: 1,
          received_at: "2026-01-07T00:00:30Z",
          text: "Done.",
        },
      ],
    }),
  },
  "mail.send": {
    request: exampleMap({
      project_key: "/data/projects/ntm",
      sender_name: "BlueLake",
      to: ["GreenCastle"],
      subject: "OpenAPI generator updates",
      body_md: "Generated docs/openapi.json from parity matrix.",
      importance: "normal",
      ack_required: true,
    }),
    response: exampleMap({
      success: true,
      count: 1,
      deliveries: [
        {
          project: "/data/projects/ntm",
          payload: {
            id: 4021,
            subject: "OpenAPI generator updates",
            thread_id: "FEAT-OPENAPI",
          },
        },
      ],
    }),
  },
  "mail.inbox": {
    response: exampleMap({
      success: true,
      messages: [
        {
          id: 4021,
          subject: "OpenAPI generator updates",
          from: "BlueLake",
          created_ts: "2026-01-07T00:00:00Z",
          importance: "normal",
          ack_required: true,
        },
      ],
    }),
  },
  "robot.mail": {
    response: exampleMap({
      success: true,
      timestamp: "2026-01-07T00:00:00Z",
      inbox: [
        {
          id: 4021,
          subject: "OpenAPI generator updates",
          from: "BlueLake",
          created_ts: "2026-01-07T00:00:00Z",
          ack_required: true,
        },
      ],
    }),
  },
  "robot.snapshot": {
    response: exampleMap({
      success: true,
      timestamp: "2026-01-07T00:00:00Z",
      sessions: [
        {
          id: "sess_proj",
          name: "ntm",
          panes: 4,
          status: "active",
        },
      ],
      panes: [
        {
          session_id: "sess_proj",
          pane: 0,
          title: "shell",
          cwd: "/data/projects/ntm",
          busy: false,
        },
        {
          session_id: "sess_proj",
          pane: 1,
          title: "claude",
          cwd: "/data/projects/ntm",
          busy: true,
        },
      ],
      agents: [
        {
          name: "BlueLake",
          type: "claude",
          model: "opus-4.1",
          pane: 1,
          status: "running",
        },
      ],
      alerts: [],
    }),
  },
  "robot.tail": {
    response: exampleMap({
      success: true,
      timestamp: "2026-01-07T00:00:00Z",
      panes: [
        {
          session: "ntm",
          pane: 1,
          lines: [
            "[INFO] Starting openapi generation...",
            "Wrote 223 endpoints to docs/parity_matrix.json",
            "Wrote OpenAPI spec to docs/openapi.json",
          ],
        },
      ],
    }),
  },
  "cass.status": {
    response: exampleMap({
      success: true,
      status: "ready",
      index_path: "/data/projects/ntm/.cass/index",
      entries: 1824,
      last_indexed_at: "2026-01-07T00:00:00Z",
      version: "0.9.2",
    }),
  },
  "cass.search": {
    response: exampleMap({
      success: true,
      count: 2,
      results: [
        {
          session_path: "/data/cass/sessions/2026-01-04-ntm.jsonl",
          agent: "claude",
          snippet: "robot snapshot output includes sessions, panes, agents",
          score: 0.82,
          created_at: "2026-01-04T18:44:05Z",
        },
        {
          session_path: "/data/cass/sessions/2026-01-02-ntm.jsonl",
          agent: "codex",
          snippet: "openapi generator writes docs/openapi.json",
          score: 0.75,
          created_at: "2026-01-02T21:09:33Z",
        },
      ],
    }),
  },
  "cass.preview": {
    response: exampleMap({
      success: true,
      session_path: "/data/cass/sessions/2026-01-04-ntm.jsonl",
      offset: 42,
      lines: [
        "{\"role\":\"assistant\",\"content\":\"Generated openapi.json\"}",
        "{\"role\":\"user\",\"content\":\"Ship it\"}",
      ],
    }),
  },
  "cass.insights": {
    response: exampleMap({
      success: true,
      summary: "Recent sessions focus on robot-mode automation.",
      top_terms: ["robot", "openapi", "parity", "agents"],
      top_agents: [
        { type: "claude", count: 12 },
        { type: "codex", count: 6 },
      ],
    }),
  },
  "cass.timeline": {
    response: exampleMap({
      success: true,
      events: [
        {
          timestamp: "2026-01-05T12:10:00Z",
          kind: "index",
          detail: "Indexed 124 sessions.",
        },
        {
          timestamp: "2026-01-06T08:30:00Z",
          kind: "prune",
          detail: "Removed 2 expired sessions.",
        },
      ],
    }),
  },
  "beads.daemon.start": {
    response: exampleMap({
      success: true,
      action: "start",
      status: "running",
      pid: 41291,
    }),
  },
  "beads.daemon.stop": {
    response: exampleMap({
      success: true,
      action: "stop",
      status: "stopped",
    }),
  },
  "beads.daemon.status": {
    response: exampleMap({
      success: true,
      running: true,
      pid: 41291,
      uptime_sec: 4200,
      socket: "/tmp/beads.sock",
      version: "0.5.1",
    }),
  },
  "beads.daemon.health": {
    response: exampleMap({
      success: true,
      status: "ok",
      message: "beads daemon healthy",
    }),
  },
  "beads.daemon.metrics": {
    response: exampleMap({
      success: true,
      metrics: {
        queue_depth: 0,
        events_processed: 184,
        last_sync_ms: 22,
      },
    }),
  },
  "robot.beads.list": {
    response: exampleMap({
      success: true,
      timestamp: "2026-01-07T00:00:00Z",
      beads: [
        {
          id: "bd-101",
          title: "Add OpenAPI generator",
          status: "done",
          priority: 2,
        },
        {
          id: "bd-104",
          title: "Wire OpenAPI lint into CI",
          status: "in_progress",
          priority: 1,
        },
      ],
    }),
  },
};

const schemaOverrides = {
  SuccessResponse: {
    type: "object",
    properties: {
      success: { type: "boolean" },
      message: { type: "string" },
      timestamp: { type: "string", format: "date-time" },
    },
    required: ["success"],
  },
  GenericResponse: {
    type: "object",
    properties: {
      success: { type: "boolean" },
      message: { type: "string" },
      warnings: { type: "array", items: { type: "string" } },
      timestamp: { type: "string", format: "date-time" },
    },
    required: ["success"],
    additionalProperties: true,
  },
  HealthResponse: {
    type: "object",
    properties: {
      success: { type: "boolean" },
      status: { type: "string" },
      version: { type: "string" },
      uptime_sec: { type: "number" },
      timestamp: { type: "string", format: "date-time" },
    },
    required: ["success", "status"],
  },
  VersionResponse: {
    type: "object",
    properties: {
      success: { type: "boolean" },
      version: { type: "string" },
      commit: { type: "string" },
      build_date: { type: "string" },
      go_version: { type: "string" },
    },
    required: ["success", "version"],
  },
  DoctorResponse: {
    type: "object",
    properties: {
      success: { type: "boolean" },
      summary: { type: "string" },
      checks: {
        type: "array",
        items: {
          type: "object",
          properties: {
            name: { type: "string" },
            status: { type: "string" },
            message: { type: "string" },
          },
          required: ["name", "status"],
        },
      },
    },
    required: ["success"],
  },
  Session: {
    type: "object",
    properties: {
      id: { type: "string" },
      name: { type: "string" },
      project_path: { type: "string" },
      created_at: { type: "string", format: "date-time" },
      status: { type: "string" },
      pane_count: { type: "number" },
      active_pane: { type: "number" },
      layout: { type: "string" },
      tmux_session: { type: "string" },
      tags: { type: "array", items: { type: "string" } },
    },
    required: ["id", "name", "project_path", "status"],
  },
  SessionResponse: {
    type: "object",
    properties: {
      success: { type: "boolean" },
      session: { $ref: "#/components/schemas/Session" },
    },
    required: ["success", "session"],
  },
  SessionListResponse: {
    type: "object",
    properties: {
      success: { type: "boolean" },
      count: { type: "number" },
      sessions: {
        type: "array",
        items: { $ref: "#/components/schemas/Session" },
      },
    },
    required: ["success", "sessions"],
  },
  CreateSessionRequest: {
    type: "object",
    properties: {
      name: { type: "string" },
      project_dir: { type: "string" },
      panes: { type: "number" },
      layout: { type: "string" },
      agents: {
        type: "array",
        items: {
          type: "object",
          properties: {
            type: { type: "string" },
            model: { type: "string" },
            pane: { type: "number" },
          },
        },
      },
      env: { type: "object", additionalProperties: { type: "string" } },
    },
    required: ["name", "project_dir"],
  },
  CreateSessionResponse: {
    type: "object",
    properties: {
      success: { type: "boolean" },
      session: { $ref: "#/components/schemas/Session" },
      pane_count: { type: "number" },
    },
    required: ["success", "session"],
  },
  SpawnRequest: {
    type: "object",
    properties: {
      agent_type: { type: "string" },
      model: { type: "string" },
      count: { type: "number" },
      panes: { type: "array", items: { type: "number" } },
      prompt: { type: "string" },
      env: { type: "object", additionalProperties: { type: "string" } },
    },
  },
  SpawnResponse: {
    type: "object",
    properties: {
      success: { type: "boolean" },
      spawned: { type: "number" },
      agents: {
        type: "array",
        items: {
          type: "object",
          properties: {
            name: { type: "string" },
            type: { type: "string" },
            model: { type: "string" },
            pane: { type: "number" },
          },
        },
      },
    },
    required: ["success"],
  },
  SendRequest: {
    type: "object",
    properties: {
      pane: { type: "number" },
      text: { type: "string" },
      enter: { type: "boolean" },
      raw: { type: "boolean" },
    },
    required: ["pane", "text"],
  },
  SendResponse: {
    type: "object",
    properties: {
      success: { type: "boolean" },
      pane: { type: "number" },
      bytes: { type: "number" },
      echoed: { type: "boolean" },
      timestamp: { type: "string", format: "date-time" },
    },
    required: ["success"],
  },
  InterruptRequest: {
    type: "object",
    properties: {
      pane: { type: "number" },
      signal: { type: "string" },
      reason: { type: "string" },
    },
  },
  InterruptResponse: {
    type: "object",
    properties: {
      success: { type: "boolean" },
      pane: { type: "number" },
      signal: { type: "string" },
    },
  },
  RobotRequest: {
    type: "object",
    properties: {
      session: { type: "string" },
      panes: { type: "array", items: { type: "number" } },
      msg: { type: "string" },
      type: { type: "string" },
      lines: { type: "number" },
      since: { type: "string" },
      track: { type: "boolean" },
      ack_timeout: { type: "string" },
    },
    additionalProperties: true,
  },
  RobotResponse: {
    type: "object",
    properties: {
      success: { type: "boolean" },
      timestamp: { type: "string", format: "date-time" },
      sessions: { type: "array", items: { type: "object" } },
      panes: { type: "array", items: { type: "object" } },
      agents: { type: "array", items: { type: "object" } },
      alerts: { type: "array", items: { type: "object" } },
      notes: { type: "array", items: { type: "string" } },
      warnings: { type: "array", items: { type: "string" } },
    },
    required: ["success"],
    additionalProperties: true,
  },
  AgentsListResponse: {
    type: "object",
    properties: {
      success: { type: "boolean" },
      agents: {
        type: "array",
        items: {
          type: "object",
          properties: {
            type: { type: "string" },
            display_name: { type: "string" },
            models: { type: "array", items: { type: "string" } },
            capabilities: { type: "array", items: { type: "string" } },
          },
        },
      },
    },
    required: ["success", "agents"],
  },
  AgentDetailResponse: {
    type: "object",
    properties: {
      success: { type: "boolean" },
      agent: { type: "object" },
    },
  },
  AgentStatsResponse: {
    type: "object",
    properties: {
      success: { type: "boolean" },
      stats: { type: "object" },
    },
  },
  AgentRecommendResponse: {
    type: "object",
    properties: {
      success: { type: "boolean" },
      recommendations: { type: "array", items: { type: "object" } },
    },
  },
  CassStatusResponse: {
    type: "object",
    properties: {
      success: { type: "boolean" },
      status: { type: "string" },
      index_path: { type: "string" },
      entries: { type: "number" },
      last_indexed_at: { type: "string", format: "date-time" },
      version: { type: "string" },
    },
    required: ["success", "status"],
  },
  CassSearchResponse: {
    type: "object",
    properties: {
      success: { type: "boolean" },
      count: { type: "number" },
      results: {
        type: "array",
        items: {
          type: "object",
          properties: {
            session_path: { type: "string" },
            agent: { type: "string" },
            snippet: { type: "string" },
            score: { type: "number" },
            created_at: { type: "string", format: "date-time" },
          },
        },
      },
    },
    required: ["success", "results"],
  },
  CassInsightsResponse: {
    type: "object",
    properties: {
      success: { type: "boolean" },
      summary: { type: "string" },
      top_terms: { type: "array", items: { type: "string" } },
      top_agents: { type: "array", items: { type: "object" } },
    },
    required: ["success"],
  },
  CassTimelineResponse: {
    type: "object",
    properties: {
      success: { type: "boolean" },
      events: { type: "array", items: { type: "object" } },
    },
    required: ["success", "events"],
  },
  CassPreviewResponse: {
    type: "object",
    properties: {
      success: { type: "boolean" },
      session_path: { type: "string" },
      offset: { type: "number" },
      lines: { type: "array", items: { type: "string" } },
    },
    required: ["success", "lines"],
  },
  DaemonStatusResponse: {
    type: "object",
    properties: {
      success: { type: "boolean" },
      running: { type: "boolean" },
      pid: { type: "number" },
      uptime_sec: { type: "number" },
      socket: { type: "string" },
      version: { type: "string" },
      last_error: { type: "string" },
    },
    required: ["success", "running"],
  },
  BeadsDaemonControlResponse: {
    type: "object",
    properties: {
      success: { type: "boolean" },
      action: { type: "string" },
      status: { type: "string" },
      pid: { type: "number" },
    },
    required: ["success", "action", "status"],
  },
  BeadsDaemonHealthResponse: {
    type: "object",
    properties: {
      success: { type: "boolean" },
      status: { type: "string" },
      message: { type: "string" },
    },
    required: ["success", "status"],
  },
  BeadsDaemonMetricsResponse: {
    type: "object",
    properties: {
      success: { type: "boolean" },
      metrics: { type: "object", additionalProperties: { type: "number" } },
    },
    required: ["success", "metrics"],
  },
  MailSendRequest: {
    type: "object",
    properties: {
      project_key: { type: "string" },
      sender_name: { type: "string" },
      to: { type: "array", items: { type: "string" } },
      subject: { type: "string" },
      body_md: { type: "string" },
      importance: { type: "string" },
      ack_required: { type: "boolean" },
      cc: { type: "array", items: { type: "string" } },
      bcc: { type: "array", items: { type: "string" } },
      attachment_paths: { type: "array", items: { type: "string" } },
    },
    required: ["project_key", "sender_name", "to", "subject", "body_md"],
  },
  MailSendResponse: {
    type: "object",
    properties: {
      success: { type: "boolean" },
      count: { type: "number" },
      deliveries: { type: "array", items: { type: "object" } },
    },
  },
  MailInboxResponse: {
    type: "object",
    properties: {
      success: { type: "boolean" },
      messages: { type: "array", items: { type: "object" } },
    },
  },
  AuthLoginRequest: {
    type: "object",
    properties: {
      provider: { type: "string" },
      username: { type: "string" },
      password: { type: "string" },
      otp: { type: "string" },
      device_name: { type: "string" },
    },
    required: ["provider", "username", "password"],
  },
  AuthLoginResponse: {
    type: "object",
    properties: {
      success: { type: "boolean" },
      token_type: { type: "string" },
      access_token: { type: "string" },
      refresh_token: { type: "string" },
      expires_in: { type: "number" },
      user: { type: "object" },
    },
    required: ["success", "access_token", "refresh_token"],
  },
  AuthRefreshRequest: {
    type: "object",
    properties: {
      refresh_token: { type: "string" },
    },
    required: ["refresh_token"],
  },
  AuthRefreshResponse: {
    type: "object",
    properties: {
      success: { type: "boolean" },
      token_type: { type: "string" },
      access_token: { type: "string" },
      expires_in: { type: "number" },
    },
    required: ["success", "access_token"],
  },
  AuthLogoutRequest: {
    type: "object",
    properties: {
      refresh_token: { type: "string" },
    },
    required: ["refresh_token"],
  },
  AuthWhoamiResponse: {
    type: "object",
    properties: {
      success: { type: "boolean" },
      user: { type: "object" },
      org: { type: "object" },
    },
  },
};

const buildSchemaMap = (schemaNames) => {
  const schemas = { ...schemaOverrides };
  for (const name of schemaNames) {
    if (!schemas[name]) {
      const isRequest = name.endsWith("Request");
      const isResponse = name.endsWith("Response");
      if (isRequest) {
        schemas[name] = {
          type: "object",
          description: `Request payload for ${name}.`,
          additionalProperties: true,
        };
      } else if (isResponse) {
        schemas[name] = {
          type: "object",
          description: `Response payload for ${name}.`,
          properties: {
            success: { type: "boolean" },
            timestamp: { type: "string", format: "date-time" },
          },
          additionalProperties: true,
        };
      } else {
        schemas[name] = {
          type: "object",
          description: `Schema ${name}.`,
          additionalProperties: true,
        };
      }
    }
  }
  return schemas;
};

const buildOpenAPI = (endpoints, schemas) => {
  const paths = {};
  for (const ep of endpoints) {
    if (!paths[ep.path]) {
      paths[ep.path] = {};
    }
    const method = ep.method.toLowerCase();
    const examples = exampleOverrides[ep.id] || {};
    const parameters = addPathParams(ep.path);
    const op = {
      tags: ep.tags || ["general"],
      summary: ep.summary,
      description: ep.description,
      parameters: parameters.length ? parameters : undefined,
      responses: {
        "200": {
          description: "OK",
          content: {
            "application/json": {
              schema: ep.responseSchema
                ? { $ref: `#/components/schemas/${ep.responseSchema}` }
                : { $ref: "#/components/schemas/GenericResponse" },
              examples:
                examples.response ||
                exampleMap({ success: true, timestamp: "2026-01-07T00:00:00Z" }),
            },
          },
        },
      },
    };
    if (ep.requestSchema) {
      op.requestBody = {
        required: true,
        content: {
          "application/json": {
            schema: { $ref: `#/components/schemas/${ep.requestSchema}` },
            examples: examples.request || exampleMap({}),
          },
        },
      };
    }
    paths[ep.path][method] = op;
  }

  return {
    openapi: "3.1.0",
    info: {
      title: "NTM REST API",
      version: "0.1.0-draft",
      description:
        "Generated from docs/parity_matrix.json. Draft OpenAPI 3.1 spec covering CLI/TUI/robot parity.",
    },
    servers: [{ url: "http://localhost:8080" }],
    paths,
    components: { schemas },
  };
};

const baseParity = readJSON(parityPath);
const baseEndpoints = baseParity && Array.isArray(baseParity.endpoints) ? baseParity.endpoints : [];

const endpoints = mergeEndpoints(baseEndpoints, extraEndpoints).map((ep) => {
  switch (ep.id) {
    case "agents.list":
      return { ...ep, responseSchema: "AgentsListResponse" };
    case "agents.show":
      return { ...ep, responseSchema: "AgentDetailResponse" };
    case "agents.stats":
      return { ...ep, responseSchema: "AgentStatsResponse" };
    case "agents.recommend":
      return { ...ep, responseSchema: "AgentRecommendResponse" };
    case "mail.send":
      return { ...ep, responseSchema: "MailSendResponse" };
    case "mail.inbox":
      return { ...ep, responseSchema: "MailInboxResponse" };
    case "cass.status":
      return { ...ep, responseSchema: "CassStatusResponse" };
    case "cass.insights":
      return { ...ep, responseSchema: "CassInsightsResponse" };
    case "cass.timeline":
      return { ...ep, responseSchema: "CassTimelineResponse" };
    case "cass.preview":
      return { ...ep, responseSchema: "CassPreviewResponse" };
    case "beads.daemon.start":
    case "beads.daemon.stop":
      return { ...ep, responseSchema: "BeadsDaemonControlResponse" };
    case "beads.daemon.health":
      return { ...ep, responseSchema: "BeadsDaemonHealthResponse" };
    case "beads.daemon.metrics":
      return { ...ep, responseSchema: "BeadsDaemonMetricsResponse" };
    default:
      return ep;
  }
});

const updatedParity = {
  generated_at: now,
  count: endpoints.length,
  endpoints,
};

const allSchemas = new Set();
for (const ep of endpoints) {
  if (ep.requestSchema) allSchemas.add(ep.requestSchema);
  if (ep.responseSchema) allSchemas.add(ep.responseSchema);
}

const schemas = buildSchemaMap(allSchemas);

const openapi = buildOpenAPI(endpoints, schemas);

writeJSON(parityPath, updatedParity);
writeJSON(openapiPath, openapi);

console.log(`Wrote ${endpoints.length} endpoints to ${parityPath}`);
console.log(`Wrote OpenAPI spec to ${openapiPath}`);
