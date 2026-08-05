/** Client helpers for the Go backend API (same origin in production). */

export type WizardSession = {
  id: string;
  current_step: number;
  status: string;
  topic: string;
  category: string;
  cost_micros: number;
  hook_confirm_ms?: number;
  state: {
    topic_cards?: { id: string; title: string; rationale: string }[];
    hook_drafts?: { id: string; direction: string; hook_text: string; drop_off_reasons: string[] }[];
    output_uri?: string;
    invalidated_segs?: number;
    total_segs?: number;
  };
};

const base = '';

export async function createWizardSession(body: {
  topic: string;
  category: string;
  accounts: { platform: string; handle: string }[];
}): Promise<WizardSession> {
  const res = await fetch(`${base}/v1/wizard/sessions`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  });
  if (!res.ok) throw new Error(await res.text());
  return res.json();
}

export async function getWizardSession(id: string): Promise<WizardSession> {
  const res = await fetch(`${base}/v1/wizard/sessions/${id}`);
  if (!res.ok) throw new Error(await res.text());
  return res.json();
}

export async function advanceWizardSession(
  id: string,
  body: Record<string, unknown> = {},
): Promise<WizardSession> {
  const res = await fetch(`${base}/v1/wizard/sessions/${id}/advance`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  });
  if (!res.ok) throw new Error(await res.text());
  return res.json();
}
