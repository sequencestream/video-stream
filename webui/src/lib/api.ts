/** Client helpers for the Go backend API (same origin in production). */

export type WizardSession = {
  id: string;
  current_step: number;
  status: string;
  topic: string;
  category: string;
  cost_micros: number;
  version: number;
  failed_step?: number;
  error?: string;
  hook_confirm_ms?: number;
  state: {
    topic_cards?: { id: string; title: string; rationale: string }[];
    hook_drafts?: { id: string; direction: string; hook_text: string; drop_off_reasons: string[] }[];
    output_uri?: string;
    invalidated_segs?: number;
    total_segs?: number;
    cost_plan?: {
      estimated_micros: number;
      budget_micros: number;
      degradation_level: number;
      decisions?: { level: number; action: string; reason: string; saved_micros: number }[];
    };
  };
};

export class WizardAPIError extends Error {
  constructor(public code: string, message: string, public session?: WizardSession) {
    super(message);
  }
}

const base = '';

export async function createWizardSession(body: {
  operation_id: string;
  topic: string;
  category: string;
  accounts: { platform: string; handle: string }[];
}): Promise<WizardSession> {
  const res = await fetch(`${base}/v1/wizard/sessions`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  });
  if (!res.ok) throw await wizardError(res);
  return res.json();
}

export async function getWizardSession(id: string): Promise<WizardSession> {
  const res = await fetch(`${base}/v1/wizard/sessions/${id}`);
  if (!res.ok) throw await wizardError(res);
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
  if (!res.ok) throw await wizardError(res);
  return res.json();
}

async function wizardError(res: Response): Promise<WizardAPIError> {
  try {
    const body = await res.json() as { code?: string; message?: string; session?: WizardSession };
    return new WizardAPIError(body.code ?? 'request_failed', body.message ?? res.statusText, body.session);
  } catch {
    return new WizardAPIError('request_failed', `${res.status} ${res.statusText}`);
  }
}
