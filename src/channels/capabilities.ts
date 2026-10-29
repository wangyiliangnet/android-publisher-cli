import { CHANNELS, type Artifact, type Channel } from '../model.js';

export const channelCapabilities: Record<Channel, {
  name: string;
  review_query: boolean;
  availability_query: boolean;
  release_notes_max: number;
  query_note: string;
}> = {
  huawei: { name: '华为', review_query: true, availability_query: true, release_notes_max: 500,
    query_note: '需要本次受理记录和匹配的版本。' },
  honor: { name: '荣耀', review_query: true, availability_query: false, release_notes_max: 500,
    query_note: '审核须匹配本次 releaseId；上架结果需在后台核对。' },
  xiaomi: { name: '小米', review_query: false, availability_query: false, release_notes_max: 500,
    query_note: '包查询不能确认本次审核和上架，需在后台核对。' },
  oppo: { name: 'OPPO', review_query: true, availability_query: true, release_notes_max: 500,
    query_note: '需要匹配本次版本、APK MD5 和版本标识。' },
  vivo: { name: 'vivo', review_query: true, availability_query: true, release_notes_max: 200,
    query_note: '需要匹配本次版本。' },
  tencent: { name: '应用宝', review_query: false, availability_query: false, release_notes_max: 500,
    query_note: '审核查询缺少本次版本标识，审核和上架需在后台核对。' },
};

export function protocols() {
  return CHANNELS.map(channel => ({ channel, ...channelCapabilities[channel],
    protocol_mock_verified: true, account_verified: false, live_verified: false,
    write_reconciliation: false }));
}

export function protocolsText(): string {
  const rows = ['渠道能力（当前实现）',
    '六渠道均只通过模拟接口测试，尚未完成真实账号与公开上架验证。',
    '均只更新已有应用，每渠道一个 APK，审核通过后立即上架。', '',
    '| 渠道 | 本次审核查询 | 本次上架确认 | 更新说明上限 |',
    '| --- | --- | --- | --- |'];
  for (const channel of CHANNELS) {
    const capability = channelCapabilities[channel];
    rows.push(`| ${capability.name}（${channel}） | ${capability.review_query ? '支持' : '后台核对'} | ` +
      `${capability.availability_query ? '支持' : '后台核对'} | ${capability.release_notes_max} 字符 |`);
  }
  rows.push('', ...CHANNELS.map(channel => `${channelCapabilities[channel].name}：${channelCapabilities[channel].query_note}`),
    '', '查询仅在本机已有本次受理记录且远端结果关联本次版本时确认状态。',
    '六渠道均不能自动核对响应丢失的写入；结果未决时继续阻塞冲突发布。');
  return `${rows.join('\n')}\n`;
}

export function validateReleaseNotes(notes: string, channels: readonly Channel[]): void {
  const length = [...notes].length;
  if ([...notes.trim()].length < 5 || /[\u0000-\u0008\u000b\u000c\u000e-\u001f\u007f]/u.test(notes))
    throw new Error('更新说明去掉首尾空白后须至少有 5 个字符，且不能含控制字符');
  for (const channel of channels) {
    const { name, release_notes_max: max } = channelCapabilities[channel];
    if (length > max) throw new Error(`${name}（${channel}）更新说明最多 ${max} 个字符，当前 ${length} 个字符；请修改后重新 prepare`);
  }
}

export function validateChannelArtifact(channel: Channel, artifact: Pick<Artifact, 'abis'>): void {
  if (channel === 'tencent' && (!artifact.abis.length ||
    artifact.abis.some(abi => !['armeabi-v7a', 'x86', 'arm64-v8a', 'x86_64'].includes(abi))))
    throw new Error('应用宝（tencent）APK 须包含受支持的 CPU 架构：armeabi-v7a、x86、arm64-v8a 或 x86_64');
}
