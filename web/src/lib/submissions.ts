import { http } from "@/lib/api";

export interface SubmissionField {
  id: string;
  label: string;
  instruction: string;
  required: boolean;
  value: string;
  sources: string[];
}
export interface SubmissionProfile {
  id: string;
  revision: number;
  name: string;
  channel: "hackerone" | "web";
  destination: string;
  source_url: string;
  rules: string;
  language: "en" | "zh";
  fields: SubmissionField[];
  updated_at?: string;
}
export interface SubmissionSource {
  finding_id: string;
  name: string;
  severity: string;
  status: string;
  summary: string;
  evidence: string;
  report: string;
  evidence_version: number;
}
export interface SubmissionDraft {
  id: string;
  revision: number;
  finding_id: string;
  profile_id: string;
  profile: SubmissionProfile;
  source: SubmissionSource;
  source_hash: string;
  title: string;
  body: string;
  impact: string;
  fields: SubmissionField[];
  warnings: string[];
  state: "draft" | "ready";
  model: string;
  updated_at: string;
  reviewed_at: string | null;
}
export interface SubmissionCheck {
  code: string;
  message: string;
  blocking: boolean;
}
export interface SubmissionView {
  draft: SubmissionDraft;
  checks: SubmissionCheck[];
}
const json = (method: string, value: unknown): RequestInit => ({ method, body: JSON.stringify(value) });
export const submissions = {
  profiles: () => http<{ profiles: SubmissionProfile[] }>("/submission-profiles").then((r) => r.profiles),
  saveProfile: (profile: SubmissionProfile) => http<SubmissionProfile>("/submission-profiles", json("PUT", profile)),
  prepare: (findingId: string, profileId: string) =>
    http<SubmissionView>(
      `/exploration/findings/${encodeURIComponent(findingId)}/submission`,
      json("POST", { profile_id: profileId }),
    ),
  load: (id: string) => http<SubmissionView>(`/submissions/${encodeURIComponent(id)}`),
  save: (draft: SubmissionDraft) =>
    http<SubmissionView>(
      `/submissions/${draft.id}`,
      json("PATCH", {
        revision: draft.revision,
        title: draft.title,
        body: draft.body,
        impact: draft.impact,
        values: Object.fromEntries(draft.fields.map((f) => [f.id, f.value])),
      }),
    ),
  generate: (draft: SubmissionDraft, modelProfileId: number) =>
    http<SubmissionView>(
      `/submissions/${draft.id}/generate`,
      json("POST", {
        revision: draft.revision,
        model_profile_id: modelProfileId,
        allow_model: true,
      }),
    ),
  confirm: (draft: SubmissionDraft) =>
    http<SubmissionView>(
      `/submissions/${draft.id}/confirm`,
      json("POST", { revision: draft.revision, reviewed: true }),
    ),
  reset: (draft: SubmissionDraft) =>
    http<SubmissionView>(`/submissions/${draft.id}/reset`, json("POST", { revision: draft.revision })),
  export: (draft: SubmissionDraft) =>
    http<{ filename: string; markdown: string; destination: string }>(
      `/submissions/${draft.id}/export?revision=${draft.revision}`,
    ),
};

export function draftChecks(view: SubmissionView): SubmissionCheck[] {
  const d = view.draft;
  const checks = view.checks.filter((c) => c.code !== "missing");
  for (const [label, value] of [
    ["标题", d.title],
    ["报告正文", d.body],
    ["实际安全影响", d.impact],
  ]) {
    if (!value.trim()) checks.push({ code: "missing", message: `待补充：${label}`, blocking: true });
  }
  for (const f of d.fields) {
    if (f.required && !f.value.trim()) checks.push({ code: "missing", message: `待补充：${f.label}`, blocking: true });
  }
  return checks;
}

export function downloadSubmission(filename: string, markdown: string) {
  const url = URL.createObjectURL(new Blob([markdown], { type: "text/markdown;charset=utf-8" }));
  const a = document.createElement("a");
  a.href = url;
  a.download = filename;
  a.click();
  setTimeout(() => URL.revokeObjectURL(url), 1000);
}
