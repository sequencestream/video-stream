'use client';

import Link from 'next/link';
import { usePathname, useSearchParams } from 'next/navigation';

import { WIZARD_STEPS } from '@/lib/wizard';

/** Horizontal step rail. Client-side so it can highlight the active route. */
export function StepNav() {
  const pathname = usePathname();
  const search = useSearchParams();
  const session = search.get('session');

  return (
    <nav aria-label="向导步骤" className="mt-6 overflow-x-auto">
      <ol className="flex min-w-max gap-2">
        {WIZARD_STEPS.map((step) => {
          const path = `/wizard/${step.id}`;
          const href = session ? `${path}?session=${encodeURIComponent(session)}` : path;
          const active = pathname === path;

          return (
            <li key={step.key}>
              <Link
                href={href}
                aria-current={active ? 'step' : undefined}
                className={[
                  'flex items-center gap-2 rounded-full border px-4 py-2 text-sm whitespace-nowrap transition-colors',
                  active
                    ? 'border-sky-500 bg-sky-500/10 text-sky-300'
                    : 'border-slate-800 text-slate-400 hover:border-slate-600 hover:text-slate-200',
                ].join(' ')}
              >
                <span className="font-mono text-xs">{step.id}</span>
                {step.title}
              </Link>
            </li>
          );
        })}
      </ol>
    </nav>
  );
}
