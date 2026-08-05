import Link from 'next/link';
import { notFound } from 'next/navigation';

import { WIZARD_STEPS, findStep, parseStepId } from '@/lib/wizard';

/** Pre-render all seven steps: the set is fixed and known at build time. */
export function generateStaticParams() {
  return WIZARD_STEPS.map((step) => ({ step: String(step.id) }));
}

export default async function WizardStepPage({ params }: { params: Promise<{ step: string }> }) {
  const { step: segment } = await params;

  const id = parseStepId(segment);
  const step = id === undefined ? undefined : findStep(id);
  if (!step) {
    notFound();
  }

  const previous = findStep(step.id - 1);
  const next = findStep(step.id + 1);

  return (
    <section>
      <p className="text-sm text-slate-500">
        第 {step.id} / {WIZARD_STEPS.length} 步
      </p>
      <h1 className="mt-1 text-2xl font-semibold tracking-tight">{step.title}</h1>
      <p className="mt-2 text-slate-400">{step.summary}</p>

      <div className="mt-8 rounded-lg border border-dashed border-slate-800 p-10 text-center">
        <p className="text-slate-400">这一步尚未实现。</p>
        <p className="mt-1 text-sm text-slate-600">
          骨架意图只交付路由与占位；<code className="font-mono">{step.key}</code> 的功能由后续意图填充。
        </p>
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
