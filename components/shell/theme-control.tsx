"use client";

import { Monitor, Moon, Sun } from "lucide-react";
import { useEffect, useState } from "react";

type Theme = "light" | "dark" | "system";

export function ThemeControl() {
  const [theme, setTheme] = useState<Theme>("system");

  useEffect(() => {
    const saved = localStorage.getItem("nerve-theme") as Theme | null;
    const next = saved === "light" || saved === "dark" ? saved : "system";
    setTheme(next);
    applyTheme(next);
  }, []);

  useEffect(() => {
    const context = (document as Document & {
      modelContext?: {
        registerTool: (tool: object, options?: { signal?: AbortSignal }) => void | Promise<void>;
      };
    }).modelContext;
    if (!context?.registerTool) return;
    const lifecycle = new AbortController();
    void Promise.resolve(context.registerTool({
      name: "set_appearance",
      title: "Set NERVE appearance",
      description: "Set the visible NERVE interface to light, dark, or system appearance and persist the preference on this device.",
      inputSchema: {
        type: "object",
        properties: { theme: { type: "string", enum: ["light", "dark", "system"] } },
        required: ["theme"],
        additionalProperties: false,
      },
      annotations: { readOnlyHint: false, untrustedContentHint: false },
      execute(input: unknown) {
        const value = (input as { theme?: unknown } | null)?.theme;
        if (value !== "light" && value !== "dark" && value !== "system") {
          throw new Error("theme must be light, dark, or system");
        }
        setTheme(value);
        applyTheme(value);
        localStorage.setItem("nerve-theme", value);
        return { theme: value };
      },
    }, { signal: lifecycle.signal })).catch(() => undefined);
    return () => lifecycle.abort();
  }, []);

  function applyTheme(next: Theme) {
    if (next === "system") document.documentElement.removeAttribute("data-theme");
    else document.documentElement.dataset.theme = next;
  }

  function cycle() {
    const next: Theme = theme === "system" ? "light" : theme === "light" ? "dark" : "system";
    setTheme(next);
    applyTheme(next);
    localStorage.setItem("nerve-theme", next);
  }

  const Icon = theme === "light" ? Sun : theme === "dark" ? Moon : Monitor;
  return <button className="icon-button" type="button" onClick={cycle} aria-label={`Theme: ${theme}. Change theme.`} title={`Theme: ${theme}`}><Icon aria-hidden="true" /></button>;
}
