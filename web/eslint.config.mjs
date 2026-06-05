import nextCoreWebVitals from "eslint-config-next/core-web-vitals";
import nextTypescript from "eslint-config-next/typescript";

const eslintConfig = [
  ...nextCoreWebVitals,
  ...nextTypescript,
  {
    ignores: ["src/lib/api/schema.d.ts"],
  },
  {
    rules: {
      // Allow unused variables with underscore prefix
      "@typescript-eslint/no-unused-vars": [
        "error",
        { argsIgnorePattern: "^_", varsIgnorePattern: "^_" },
      ],
      // This dashboard intentionally syncs selected UI state from query data in effects.
      "react-hooks/set-state-in-effect": "off",
    },
  },
];

export default eslintConfig;
