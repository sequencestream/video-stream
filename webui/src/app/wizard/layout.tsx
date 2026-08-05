import Link from 'next/link';

import { WIZARD_STEPS } from '@/lib/wizard';

import { StepNav } from './step-nav';

/**
 * Shell shared by all seven steps: the step rail stays mounted while the step
 * body swaps, so a later intent adding real content to one step cannot alter
 * the navigation of the others.
 */
export default function WizardLayout({ children }: { children: React.ReactNode }) {
  return (
    <div className="mx-auto max-w-5xl px-6 py-10">
      <header className="flex items-baseline justify-between">
        <Link href="/" className="text-lg font-semibold tracking-tight hover:text-sky-400">
          video-stream
        </Link>
        <span className="text-sm text-slate-500">共 {WIZARD_STEPS.length} 步</span>
      </header>

      <StepNav />

      <main className="mt-8">{children}</main>
    </div>
  );
}
