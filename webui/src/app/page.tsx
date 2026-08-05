import Link from 'next/link';

import { FIRST_STEP, WIZARD_STEPS } from '@/lib/wizard';

export default function HomePage() {
  return (
    <main className="mx-auto max-w-3xl px-6 py-16">
      <h1 className="text-3xl font-semibold tracking-tight">video-stream</h1>
      <p className="mt-3 text-slate-400">
        本地优先的 AI 视频生产流水线。当前为工程骨架，7 步向导为空壳页面。
      </p>

      <Link
        href={`/wizard/${FIRST_STEP.id}`}
        className="mt-8 inline-block rounded-md bg-sky-500 px-5 py-2.5 text-sm font-medium text-slate-950 hover:bg-sky-400"
      >
        开始向导
      </Link>

      <ol className="mt-12 space-y-3">
        {WIZARD_STEPS.map((step) => (
          <li key={step.key}>
            <Link
              href={`/wizard/${step.id}`}
              className="flex gap-4 rounded-lg border border-slate-800 p-4 hover:border-slate-600"
            >
              <span className="text-sm font-mono text-slate-500">
                {String(step.id).padStart(2, '0')}
              </span>
              <span>
                <span className="block font-medium">{step.title}</span>
                <span className="mt-1 block text-sm text-slate-400">{step.summary}</span>
              </span>
            </Link>
          </li>
        ))}
      </ol>
    </main>
  );
}
