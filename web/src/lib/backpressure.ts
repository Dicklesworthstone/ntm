export type PressureLevel = "normal" | "elevated" | "critical" | "overload";

export function mapDecisionToPressureLevel(decision?: string): PressureLevel {
  const normalized = (decision || "").trim().toLowerCase();
  switch (normalized) {
    case "degrade":
    case "overload":
      return "overload";
    case "defer":
    case "critical":
    case "warning":
      return "critical";
    case "coalesce":
    case "elevated":
    case "watch":
      return "elevated";
    default:
      return "normal";
  }
}
