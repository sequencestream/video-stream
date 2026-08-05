export type WizardStep = {
  readonly id: number;
  readonly key: string;
  readonly title: string;
  readonly summary: string;
};

export const WIZARD_STEPS: readonly WizardStep[] = [
  { id: 1, key: 'setup', title: '主题与对标', summary: '填写主题、类目并导入对标账号。' },
  { id: 2, key: 'topics', title: '选题卡片', summary: '从 3–5 个迁移选题中点选 1 个。' },
  { id: 3, key: 'hook', title: 'Hook 三选一', summary: '3×Writer 竞标，30 秒内三选一或改一句。' },
  { id: 4, key: 'script', title: '脚本定稿', summary: '多 Agent 打磨 2 轮，输出 seg 工程。' },
  { id: 5, key: 'assets', title: '素材与音轨', summary: '混合画面规划 + 合规校验。' },
  { id: 6, key: 'preview', title: '720p 预览', summary: '低清预览；改一句触发增量重编译。' },
  { id: 7, key: 'deliver', title: '1080p 出片', summary: '高清出片 + 合规标识 + 下载/YouTube。' },
] as const;

export const FIRST_STEP = WIZARD_STEPS[0];
export const LAST_STEP = WIZARD_STEPS[WIZARD_STEPS.length - 1];

export function findStep(id: number): WizardStep | undefined {
  return WIZARD_STEPS.find((step) => step.id === id);
}

export function parseStepId(segment: string): number | undefined {
  return /^\d+$/.test(segment) ? Number(segment) : undefined;
}
