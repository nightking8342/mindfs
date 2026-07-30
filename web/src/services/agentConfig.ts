import { appPath } from "./base";
import { protectedJSON } from "./api";

export type AgentConfigSource = {
  sourcePath: string;
  backupPath: string;
};

export type AgentConfigBackup = {
  id: string;
  agent: string;
  name: string;
  createdAt: string;
  updatedAt: string;
  sources?: AgentConfigSource[];
  envKeys?: string[];
  isolatedClaudeSettings?: boolean;
  claudeSettingsPath?: string;
};

export type AgentAPIProvider = {
  id: string;
  name: string;
  baseUrl: string;
  protocols: string[];
  modelFamilies: string[];
  models?: string[];
  createdAt: string;
  updatedAt: string;
};

export type AgentConfigDefaults = {
  agent: string;
  file_sources: string[];
  env_keys: string[];
};

export type AgentConfigFileContent = {
  source_path: string;
  content: string;
};

export async function fetchAgentConfigDefaults(agent: string): Promise<AgentConfigDefaults> {
  const params = new URLSearchParams({ agent });
  return protectedJSON<AgentConfigDefaults>(appPath(`/api/agent-config/defaults?${params.toString()}`));
}

export async function fetchAgentConfigBackups(agent: string): Promise<AgentConfigBackup[]> {
  const params = new URLSearchParams({ agent });
  return protectedJSON<AgentConfigBackup[]>(appPath(`/api/agent-config/backups?${params.toString()}`));
}

export async function createAgentConfigBackup(input: {
  agent: string;
  name: string;
  fileSources?: string[];
  envLines?: string[];
  overwrite?: boolean;
  isolatedClaudeSettings?: boolean;
  claudeSettingsPath?: string;
  fileContents?: AgentConfigFileContent[];
  claudeSettingsContent?: string;
}): Promise<AgentConfigBackup> {
  return protectedJSON<AgentConfigBackup>(appPath("/api/agent-config/backups"), {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({
      agent: input.agent,
      name: input.name,
      file_sources: input.fileSources || [],
      env_lines: input.envLines || [],
      overwrite: !!input.overwrite,
      isolated_claude_settings: input.isolatedClaudeSettings,
      claude_settings_path: input.claudeSettingsPath || "",
      file_contents: input.fileContents || [],
      claude_settings_content: input.claudeSettingsContent,
    }),
  });
}

export async function updateAgentConfigBackup(input: {
  id: string;
  fileSources?: string[];
  envLines?: string[];
  isolatedClaudeSettings?: boolean;
  claudeSettingsPath?: string;
}): Promise<AgentConfigBackup> {
  return protectedJSON<AgentConfigBackup>(appPath("/api/agent-config/backups"), {
    method: "PUT",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({
      id: input.id,
      file_sources: input.fileSources || [],
      env_lines: input.envLines || [],
      isolated_claude_settings: input.isolatedClaudeSettings,
      claude_settings_path: input.claudeSettingsPath || "",
    }),
  });
}

export async function deleteAgentConfigBackup(id: string): Promise<{ deleted: boolean; id: string; backups?: AgentConfigBackup[] }> {
  const params = new URLSearchParams({ id });
  return protectedJSON<{ deleted: boolean; id: string; backups?: AgentConfigBackup[] }>(appPath(`/api/agent-config/backups?${params.toString()}`), {
    method: "DELETE",
  });
}

export async function previewAgentConfigSourceFile(path: string): Promise<{ path: string; content: string; size: number }> {
  return protectedJSON(appPath("/api/agent-config/preview-file"), {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ path }),
  });
}

export async function fetchAgentConfigBackupFile(input: {
  id: string;
  backupPath?: string;
  kind?: "claude_settings" | "";
}): Promise<{ id: string; backup_path: string; kind?: string; content: string; size: number }> {
  const params = new URLSearchParams({ id: input.id });
  if (input.backupPath) {
    params.set("backup_path", input.backupPath);
  }
  if (input.kind) {
    params.set("kind", input.kind);
  }
  return protectedJSON(appPath(`/api/agent-config/backups/file?${params.toString()}`));
}

export async function saveAgentConfigBackupFile(input: {
  id: string;
  content: string;
  backupPath?: string;
  kind?: "claude_settings" | "";
}): Promise<{ id: string; backup_path: string; size: number }> {
  return protectedJSON(appPath("/api/agent-config/backups/file"), {
    method: "PUT",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({
      id: input.id,
      backup_path: input.backupPath || "",
      kind: input.kind || "",
      content: input.content,
    }),
  });
}

export async function fetchAgentConfigBackupEnv(id: string): Promise<{ id: string; env_lines: string[] }> {
  const params = new URLSearchParams({ id });
  return protectedJSON(appPath(`/api/agent-config/backups/env?${params.toString()}`));
}

export type AgentConfigSwitchStepKey =
  | "restore_files"
  | "claude_settings"
  | "apply_env"
  | "kill_sessions"
  | "record_selection"
  | "probe";

export type AgentConfigSwitchStep = {
  key: AgentConfigSwitchStepKey | string;
  status: "ok" | "failed" | "running" | "skipped";
  count?: number;
  target?: string;
  duration_ms: number;
  error?: string;
};

export async function switchAgentConfig(input: {
  id: string;
  confirmOverwrite?: boolean;
}): Promise<{
  needs_confirm: boolean;
  message?: string;
  backup?: AgentConfigBackup;
  steps?: AgentConfigSwitchStep[];
}> {
  return protectedJSON(appPath("/api/agent-config/switch"), {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({
      id: input.id,
      confirm_overwrite: !!input.confirmOverwrite,
    }),
  });
}

// A failed switch answers 400 with the step list attached, so the caller can
// show which stage stopped it. Returns [] when the error carries no steps.
export function agentConfigSwitchStepsFromError(error: unknown): AgentConfigSwitchStep[] {
  const payload = (error as { payload?: { steps?: unknown } } | null)?.payload;
  const steps = payload?.steps;
  return Array.isArray(steps) ? (steps as AgentConfigSwitchStep[]) : [];
}

// probe is "unknown" when no completion event arrived within the client-side
// timeout. That is not the same as failure -- the server allows the probe up to
// four minutes, so it may still be running.
export type AgentConfigSwitchProbeState = "running" | "ok" | "failed" | "unknown";

export type AgentConfigSwitchProgress = {
  agent: string;
  backupID: string;
  backupName: string;
  steps: AgentConfigSwitchStep[];
  probe: AgentConfigSwitchProbeState;
  probeError: string;
  /** Set when the switch itself failed before the probe started. */
  switchError: string;
  startedAt: number;
  finishedAt: number;
};

export const AGENT_CONFIG_SWITCH_PROBE_TIMEOUT_MS = 90_000;

export async function fetchAgentAPIProviders(agent?: string): Promise<AgentAPIProvider[]> {
  const params = new URLSearchParams();
  if (agent) {
    params.set("agent", agent);
  }
  const query = params.toString();
  return protectedJSON<AgentAPIProvider[]>(appPath(`/api/agent-api-providers${query ? `?${query}` : ""}`));
}

export async function createAgentAPIProvider(input: {
  name: string;
  baseUrl: string;
  apiKey: string;
}): Promise<AgentAPIProvider> {
  return protectedJSON<AgentAPIProvider>(appPath("/api/agent-api-providers"), {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({
      name: input.name,
      baseUrl: input.baseUrl,
      apiKey: input.apiKey,
    }),
  });
}

export async function syncAgentAPIProviders(input: Array<{
  name: string;
  baseUrl: string;
  apiKey: string;
}>): Promise<{ providers: AgentAPIProvider[] }> {
  return protectedJSON<{ providers: AgentAPIProvider[] }>(appPath("/api/agent-api-providers/sync"), {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ providers: input }),
  });
}

export async function deleteAgentAPIProvider(id: string): Promise<{ deleted: boolean; id: string; providers?: AgentAPIProvider[] }> {
  const params = new URLSearchParams({ id });
  return protectedJSON<{ deleted: boolean; id: string; providers?: AgentAPIProvider[] }>(appPath(`/api/agent-api-providers?${params.toString()}`), {
    method: "DELETE",
  });
}

export async function switchAgentAPIProvider(input: {
  agent: string;
  providerID: string;
}): Promise<{ provider?: AgentAPIProvider }> {
  return protectedJSON(appPath("/api/agent-api-providers/switch"), {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({
      agent: input.agent,
      provider_id: input.providerID,
    }),
  });
}

export function isClaudeAgentName(name: string): boolean {
  const n = String(name || "").trim().toLowerCase();
  return n === "claude" || n === "claudecode" || n === "claude-code";
}
