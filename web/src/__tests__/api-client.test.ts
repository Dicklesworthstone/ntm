/**
 * API Client Tests
 *
 * Smoke tests for the API client configuration.
 */

import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";

// Mock localStorage
const localStorageMock = {
  store: {} as Record<string, string>,
  getItem(key: string) {
    return this.store[key] || null;
  },
  setItem(key: string, value: string) {
    this.store[key] = value;
  },
  removeItem(key: string) {
    delete this.store[key];
  },
  clear() {
    this.store = {};
  },
};

Object.defineProperty(global, "localStorage", {
  value: localStorageMock,
});

// Import after mocking
import {
  ApiError,
  apiFetch,
  browserNavigation,
  checkConnection,
  clearConnectionConfig,
  extractKernelPathParams,
  getBaseUrl,
  getConnectionConfig,
  resetClient,
  resolveKernelRESTPath,
  saveConnectionConfig,
} from "../lib/api/client";

describe("API Client", () => {
  beforeEach(() => {
    localStorageMock.clear();
  });

  afterEach(() => {
    localStorageMock.clear();
  });

  describe("getConnectionConfig", () => {
    it("returns null when no config is stored", () => {
      const config = getConnectionConfig();
      expect(config).toBeNull();
    });

    it("returns stored config", () => {
      const config = { baseUrl: "http://localhost:9000", authToken: "test-token" };
      localStorageMock.setItem("ntm-connection", JSON.stringify(config));

      const result = getConnectionConfig();
      expect(result).toEqual(config);
    });

    it("normalizes trailing slashes in stored config", () => {
      localStorageMock.setItem(
        "ntm-connection",
        JSON.stringify({ baseUrl: "http://localhost:9000///", authToken: "test-token" })
      );

      const result = getConnectionConfig();
      expect(result).toEqual({
        baseUrl: "http://localhost:9000",
        authToken: "test-token",
      });
    });

    it("returns null for invalid JSON", () => {
      localStorageMock.setItem("ntm-connection", "invalid-json");

      const result = getConnectionConfig();
      expect(result).toBeNull();
    });
  });

  describe("saveConnectionConfig", () => {
    it("saves config to localStorage", () => {
      const config = { baseUrl: "http://test:8080" };
      saveConnectionConfig(config);

      const stored = JSON.parse(localStorageMock.getItem("ntm-connection") || "{}");
      expect(stored.baseUrl).toBe("http://test:8080");
    });

    it("saves config with auth token", () => {
      const config = { baseUrl: "http://test:8080", authToken: "my-token" };
      saveConnectionConfig(config);

      const stored = JSON.parse(localStorageMock.getItem("ntm-connection") || "{}");
      expect(stored.authToken).toBe("my-token");
    });

    it("normalizes trailing slashes before saving", () => {
      saveConnectionConfig({ baseUrl: "http://test:8080///", authToken: "my-token" });

      const stored = JSON.parse(localStorageMock.getItem("ntm-connection") || "{}");
      expect(stored.baseUrl).toBe("http://test:8080");
    });
  });

  describe("getBaseUrl", () => {
    it("returns a normalized stored base URL", () => {
      localStorageMock.setItem(
        "ntm-connection",
        JSON.stringify({ baseUrl: "http://localhost:7337///" })
      );

      expect(getBaseUrl()).toBe("http://localhost:7337");
    });

    it("falls back to the default URL for slash-only input", () => {
      localStorageMock.setItem(
        "ntm-connection",
        JSON.stringify({ baseUrl: "///" })
      );

      expect(getBaseUrl()).toBe("http://localhost:7337");
    });
  });

  describe("clearConnectionConfig", () => {
    it("removes config from localStorage", () => {
      localStorageMock.setItem("ntm-connection", '{"baseUrl":"http://test"}');
      clearConnectionConfig();

      expect(localStorageMock.getItem("ntm-connection")).toBeNull();
    });
  });

  describe("apiFetch & 401 /connect redirect", () => {
    afterEach(() => {
      resetClient();
      vi.restoreAllMocks();
      vi.unstubAllGlobals();
    });

    it("sends Authorization Bearer header and returns parsed envelope", async () => {
      saveConnectionConfig({
        baseUrl: "http://localhost:7337",
        authToken: "secret-bearer-123",
      });
      const fetchSpy = vi.fn(async () =>
        new Response(JSON.stringify({ success: true, sessions: [] }), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        })
      );
      vi.stubGlobal("fetch", fetchSpy);

      const data = await apiFetch<{ success: boolean; sessions: unknown[] }>(
        "/api/v1/sessions"
      );

      expect(data.success).toBe(true);
      expect(fetchSpy).toHaveBeenCalledTimes(1);
      const [url, init] = fetchSpy.mock.calls[0] as unknown as [string, RequestInit];
      expect(url).toBe("http://localhost:7337/api/v1/sessions");
      expect((init.headers as Record<string, string>).Authorization).toBe(
        "Bearer secret-bearer-123"
      );
    });

    it("redirects cleanly to /connect on 401 when on a dashboard route", async () => {
      saveConnectionConfig({
        baseUrl: "http://localhost:7337",
        authToken: "expired-token",
      });
      vi.spyOn(browserNavigation, "getPathname").mockReturnValue("/sessions");
      const redirectSpy = vi
        .spyOn(browserNavigation, "redirect")
        .mockImplementation(() => {});

      vi.stubGlobal(
        "fetch",
        vi.fn(async () =>
          new Response(
            JSON.stringify({
              success: false,
              error: "Unauthorized",
              error_code: "UNAUTHORIZED",
            }),
            { status: 401, headers: { "Content-Type": "application/json" } }
          )
        )
      );

      await expect(apiFetch("/api/v1/sessions")).rejects.toBeInstanceOf(ApiError);
      expect(redirectSpy).toHaveBeenCalledTimes(1);
      expect(redirectSpy).toHaveBeenCalledWith("/connect");
    });

    it("does NOT redirect on 401 when already on /connect or /connect/ (prevents reload loops)", async () => {
      saveConnectionConfig({
        baseUrl: "http://localhost:7337",
        authToken: "bad-token",
      });
      const redirectSpy = vi
        .spyOn(browserNavigation, "redirect")
        .mockImplementation(() => {});
      const pathnameSpy = vi
        .spyOn(browserNavigation, "getPathname")
        .mockReturnValue("/connect");

      vi.stubGlobal(
        "fetch",
        vi.fn(async () =>
          new Response(JSON.stringify({ success: false, error: "Unauthorized" }), {
            status: 401,
            headers: { "Content-Type": "application/json" },
          })
        )
      );

      await expect(apiFetch("/api/v1/health")).rejects.toBeInstanceOf(ApiError);
      expect(redirectSpy).not.toHaveBeenCalled();

      pathnameSpy.mockReturnValue("/connect/");
      resetClient();
      const conn = await checkConnection();
      expect(conn.ok).toBe(false);
      expect(redirectSpy).not.toHaveBeenCalled();
    });
  });

  describe("kernel REST path normalization and parameter interpolation", () => {
    it("extracts unique path parameters in order", () => {
      expect(extractKernelPathParams("/sessions")).toEqual([]);
      expect(extractKernelPathParams("/sessions/{sessionId}/events")).toEqual([
        "sessionId",
      ]);
      expect(
        extractKernelPathParams(
          "/sessions/{sessionId}/panes/{paneIdx}/repeat/{sessionId}"
        )
      ).toEqual(["sessionId", "paneIdx"]);
    });

    it("normalizes relative kernel paths under /api/v1 and preserves /api/* paths", () => {
      expect(resolveKernelRESTPath("/sessions")).toBe("/api/v1/sessions");
      expect(resolveKernelRESTPath("deps")).toBe("/api/v1/deps");
      expect(resolveKernelRESTPath("/version")).toBe("/api/v1/version");
      expect(resolveKernelRESTPath("/api/v1/sessions")).toBe("/api/v1/sessions");
      expect(resolveKernelRESTPath("/api/kernel/commands")).toBe(
        "/api/kernel/commands"
      );
    });

    it("interpolates path parameters using encodeURIComponent", () => {
      expect(
        resolveKernelRESTPath("/sessions/{sessionId}/events", {
          sessionId: "my/proj#1",
        })
      ).toBe("/api/v1/sessions/my%2Fproj%231/events");

      expect(
        resolveKernelRESTPath("/api/v1/sessions/{sessionId}/panes/{paneIdx}", {
          sessionId: "team alpha",
          paneIdx: "0",
        })
      ).toBe("/api/v1/sessions/team%20alpha/panes/0");
    });

    it("throws when a required path parameter is missing or blank", () => {
      expect(() =>
        resolveKernelRESTPath("/sessions/{sessionId}/events", {})
      ).toThrow("Missing required parameter: sessionId");
      expect(() =>
        resolveKernelRESTPath("/sessions/{sessionId}/events", {
          sessionId: "   ",
        })
      ).toThrow("Missing required parameter: sessionId");
    });
  });
});
