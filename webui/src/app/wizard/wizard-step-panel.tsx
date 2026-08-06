'use client';

import { useState } from 'react';
import Link from 'next/link';

import { advanceWizardSession, createWizardSession, type WizardSession } from '@/lib/api';
import { findStep, type WizardStep } from '@/lib/wizard';

type Props = {
  step: WizardStep;
  previous?: WizardStep;
  next?: WizardStep;
};

export function WizardStepPanel({ step, previous, next }: Props) {
  const [session, setSession] = useState<WizardSession | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [topic, setTopic] = useState('');
  const [category, setCategory] = useState('education');
  const [loading, setLoading] = useState(false);

  async function run(action: () => Promise<WizardSession>) {
    setLoading(true);
    setError(null);
    try {
      setSession(await action());
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setLoading(false);
    }
  }

  return (
    <section>
      <p className="text-sm text-slate-500">
        第 {step.id} / 7 步 · 会话 {session?.id ?? '（未开始）'}
      </p>
      <h1 className="mt-1 text-2xl font-semibold tracking-tight">{step.title}</h1>
      <p className="mt-2 text-slate-400">{step.summary}</p>

      {session && (
        <div className="mt-4 rounded border border-slate-800 bg-slate-900/40 p-3 text-sm text-slate-300">
          <p>状态: {session.status} · 步进: {session.current_step} · 成本: ${(session.cost_micros / 1_000_000).toFixed(3)}</p>
          {session.state.cost_plan && (
            <p className="mt-1 text-xs text-slate-400">
              预估 ${(session.state.cost_plan.estimated_micros / 1_000_000).toFixed(3)} / 预算 ${(session.state.cost_plan.budget_micros / 1_000_000).toFixed(2)}
              {session.state.cost_plan.degradation_level > 0 && ` · 降级 L${session.state.cost_plan.degradation_level}`}
            </p>
          )}
          {session.state.output_uri && <p className="mt-1 font-mono text-xs">{session.state.output_uri}</p>}
        </div>
      )}

      {error && <p className="mt-4 text-sm text-red-400">{error}</p>}

      <div className="mt-8 space-y-4">
        {step.key === 'setup' && (
          <>
            <input className="w-full rounded border border-slate-700 bg-slate-900 px-3 py-2" placeholder="主题" value={topic} onChange={(e) => setTopic(e.target.value)} />
            <input className="w-full rounded border border-slate-700 bg-slate-900 px-3 py-2" placeholder="类目" value={category} onChange={(e) => setCategory(e.target.value)} />
            <button type="button" disabled={loading || !topic} className="rounded bg-sky-600 px-4 py-2 text-sm disabled:opacity-50" onClick={() => run(() => createWizardSession({ topic, category, accounts: [{ platform: 'youtube', handle: '@peer1' }] }))}>
              开始向导
            </button>
          </>
        )}

        {step.key === 'topics' && session?.state.topic_cards && (
          <ul className="space-y-2">
            {session.state.topic_cards.map((c) => (
              <li key={c.id}>
                <button type="button" className="w-full rounded border border-slate-700 px-3 py-2 text-left hover:border-sky-500" disabled={loading} onClick={() => run(() => advanceWizardSession(session.id, { topic_card_id: c.id }))}>
                  <span className="font-medium">{c.title}</span>
                  <span className="mt-1 block text-xs text-slate-500">{c.rationale}</span>
                </button>
              </li>
            ))}
          </ul>
        )}

        {step.key === 'hook' && session?.state.hook_drafts && (
          <ul className="space-y-2">
            {session.state.hook_drafts.map((d) => (
              <li key={d.id}>
                <button type="button" className="w-full rounded border border-slate-700 px-3 py-2 text-left hover:border-sky-500" disabled={loading} onClick={() => run(() => advanceWizardSession(session.id, { draft_id: d.id }))}>
                  <span className="font-mono text-xs text-slate-500">{d.direction}</span>
                  <span className="mt-1 block">{d.hook_text}</span>
                </button>
              </li>
            ))}
          </ul>
        )}

        {['script', 'assets', 'deliver'].includes(step.key) && session && (
          <button type="button" className="rounded bg-sky-600 px-4 py-2 text-sm disabled:opacity-50" disabled={loading} onClick={() => run(() => advanceWizardSession(session.id, {}))}>
            执行 {step.title}
          </button>
        )}

        {step.key === 'preview' && session && (
          <>
            <button type="button" className="rounded bg-sky-600 px-4 py-2 text-sm disabled:opacity-50" disabled={loading} onClick={() => run(() => advanceWizardSession(session.id, { edit_seg_id: 'hook', edit_text: 'Quick edited hook for preview' }))}>
              720p 预览（含改一句重编译）
            </button>
          </>
        )}
      </div>

      <div className="mt-8 flex justify-between">
        {previous ? (
          <Link href={`/wizard/${previous.id}`} className="text-sm text-slate-400 hover:text-slate-200">
            ← {previous.title}
          </Link>
        ) : (
          <Link href="/" className="text-sm text-slate-400 hover:text-slate-200">
            ← 返回首页
          </Link>
        )}
        {next && (
          <Link href={`/wizard/${next.id}`} className="text-sm text-sky-400 hover:text-sky-300">
            {next.title} →
          </Link>
        )}
      </div>
    </section>
  );
}
