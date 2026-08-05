import { notFound } from 'next/navigation';

import { WIZARD_STEPS, findStep, parseStepId } from '@/lib/wizard';

import { WizardStepPanel } from '../wizard-step-panel';

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
  return <WizardStepPanel step={step} previous={previous} next={next} />;
}
