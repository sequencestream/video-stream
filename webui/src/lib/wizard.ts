/**
 * The seven wizard steps.
 *
 * This list is the single source of truth for the shell: routing, the step nav
 * and the per-step pages all derive from it, so a later intent adds real
 * content to a step without touching navigation.
 */
export type WizardStep = {
  /** 1-based position, also the URL segment. */
  readonly id: number;
  /** Stable machine key, used by later intents to bind their feature module. */
  readonly key: string;
  readonly title: string;
  readonly summary: string;
};

export const WIZARD_STEPS: readonly WizardStep[] = [
  {
    id: 1,
    key: 'ideation',
    title: '选题',
    summary: '从竞品雷达与结构卡片中挑选或生成一个选题。',
  },
  {
    id: 2,
    key: 'script',
    title: '脚本',
    summary: '多 Agent 打磨脚本，直到通过差异化闸门。',
  },
  {
    id: 3,
    key: 'visual',
    title: '视觉',
    summary: '选择视觉身份栈与样式包，编排混合画面。',
  },
  {
    id: 4,
    key: 'audio',
    title: '音轨与字幕',
    summary: '生成配音音轨，并对齐词级时间戳字幕。',
  },
  {
    id: 5,
    key: 'compliance',
    title: '合规',
    summary: '注入合规标识，并读回校验产物是否带标。',
  },
  {
    id: 6,
    key: 'render',
    title: '渲染',
    summary: '按 720p / 1080p 双档渲染，共享素材缓存。',
  },
  {
    id: 7,
    key: 'publish',
    title: '发布',
    summary: '发布到 YouTube 并在完成后通知。',
  },
] as const;

export const FIRST_STEP = WIZARD_STEPS[0];
export const LAST_STEP = WIZARD_STEPS[WIZARD_STEPS.length - 1];

/** Returns the step with the given id, or undefined when the id is out of range. */
export function findStep(id: number): WizardStep | undefined {
  return WIZARD_STEPS.find((step) => step.id === id);
}

/** Parses a URL segment into a step id. Returns undefined for anything unparseable. */
export function parseStepId(segment: string): number | undefined {
  return /^\d+$/.test(segment) ? Number(segment) : undefined;
}
