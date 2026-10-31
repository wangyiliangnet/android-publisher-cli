import { CHANNELS, type Channel, type Project, type ProjectChannel, isChannel, validateProject, validClientId, validId, validPackage } from './model.js';

export const channelNames: Record<Channel, string> = {
  huawei: '华为', honor: '荣耀', xiaomi: '小米', oppo: 'OPPO', vivo: 'vivo', tencent: '应用宝',
};

export interface SetupIO {
  ask(label: string, fallback?: string): Promise<string>;
  write(message: string): void;
}

export async function configureProject(projectId: string, io: SetupIO, previous?: Project): Promise<Project> {
  if (!validId(projectId)) throw new Error('应用配置名只能包含字母、数字、点、横线和下划线，须以字母或数字开头，最多 128 个字符');
  const ask = async (label: string, fallback = ''): Promise<string> => {
    const typed = (await io.ask(label, fallback)).trim();
    if (typed.toLowerCase() === 'cancel') throw new Error('已取消配置，未保存应用配置');
    return typed || fallback;
  };
  const required = async (label: string, fallback = ''): Promise<string> => {
    for (;;) {
      const value = await ask(label, fallback);
      if (validId(value)) return value;
      io.write('此项必填，只能包含字母、数字、点、横线和下划线，须以字母或数字开头，最多 128 个字符。\n');
    }
  };
  io.write(`配置应用 ${projectId}：保存应用在各商店的资料，供以后发布更新时复用。\n` +
    '此处不上传 APK。密钥会在连接渠道时另行提示输入。输入 cancel 可取消。\n' +
    CHANNELS.map(channel => `${channel}=${channelNames[channel]}`).join('，') + '\n');
  let selection: Channel[];
  for (;;) {
    const names = (await ask('要发布到哪些渠道？输入英文名称，用逗号分隔', previous?.channels.map(row => row.channel).join(','))).split(',').map(name => name.trim());
    if (names.length && names.every(isChannel) && new Set(names).size === names.length) { selection = names as Channel[]; break; }
    io.write('请从上方渠道中选择，名称不能重复。例如只发布到华为时输入 huawei。\n');
  }
  let packageName = '';
  {
    do {
      packageName = await ask('应用包名（如 com.example.app；所有渠道包须与此一致）',
        previous?.package_name ?? previous?.channels.find(row => ['xiaomi', 'oppo', 'vivo'].includes(row.channel))?.app_id);
      if (!validPackage(packageName)) io.write('请输入应用包名，例如 com.example.app。首次配置无需提供 APK。\n');
    } while (!validPackage(packageName));
  }
  const rows: ProjectChannel[] = [];
  io.write('\n账号备注由你命名，用来区分本机保存的渠道账号；同一渠道账号请始终使用同一备注。只有一个账号时直接回车。\n');
  for (const channel of selection) {
    const saved = previous?.channels.find(row => row.channel === channel);
    io.write(`\n${channelNames[channel]}（${channel}）\n`);
    let account: string;
    {
      for (;;) {
        account = await ask('账号备注', saved?.account || `${channel}-main`);
        if (validId(account)) break;
        io.write('账号备注只能包含字母、数字、点、横线和下划线，须以字母或数字开头，最多 128 个字符。\n');
      }
    }
    const appId = ['xiaomi', 'oppo', 'vivo'].includes(channel) ? packageName
      : await required(`${channelNames[channel]} App ID（从该应用的商店后台复制，不是包名或登录账号）`, saved?.app_id);
    const row: ProjectChannel = { channel, account, app_id: appId };
    const labels: Partial<Record<Channel, string>> = {
      honor: '荣耀 client_id（API 客户端 ID，与 client_secret 配对；从后台 API 接入资料复制）',
      oppo: 'OPPO client_id（从开发者后台的 API 接入资料复制）',
      xiaomi: '小米 userName（自动发布 API 使用的开发者账号）',
      vivo: 'vivo access_key（API 接入标识，与 access_secret 配对；从后台复制）',
      tencent: '应用宝 uid（主开发者账号的用户 ID，不是应用 AppID）',
    };
    if (labels[channel]) {
      if (channel === 'xiaomi') {
        for (;;) {
          const value = await ask(labels[channel]!, saved?.client_id);
          if (validClientId(channel, value)) { row.client_id = value; break; }
          io.write('请填写小米开发者账号（userName），最多 256 字节，不含控制字符。\n');
        }
      } else row.client_id = await required(labels[channel]!, saved?.client_id);
    }
    if (saved?.language_id) row.language_id = saved.language_id;
    if (channel === 'huawei' || channel === 'honor') io.write(saved?.language_id
      ? `保留已有更新说明语言设置：${saved.language_id}。\n` : '更新说明使用后台已有的简体中文条目，预检时自动查找。\n');
    if (channel === 'xiaomi') {
      row.icon_path = await ask('应用图标文件的绝对路径（小米 API 提交时需要）', saved?.icon_path);
      row.privacy_url = await ask('应用隐私政策的 HTTPS 网页地址（不带查询参数或 #片段）', saved?.privacy_url);
    }
    rows.push(row);
  }
  const project: Project = { schema_version: 2, project_id: projectId, package_name: packageName, channels: rows };
  if (previous?.display_name) project.display_name = previous.display_name;
  validateProject(project);
  io.write(`\n即将保存的应用配置：${projectId}\n`);
  for (const row of rows) {
    const clientNames: Partial<Record<Channel, string>> = { honor: 'client_id', xiaomi: 'userName', vivo: 'access_key', tencent: 'uid' };
    io.write(`${channelNames[row.channel]}：${['xiaomi', 'oppo', 'vivo'].includes(row.channel) ? '包名' : 'App ID'} ${row.app_id}；` +
      `账号备注 ${row.account}${row.client_id ? `；${clientNames[row.channel] ?? 'client_id'} ${row.client_id}` : ''}\n`);
    if (row.icon_path) io.write(`  图标：${row.icon_path}\n  隐私政策：${row.privacy_url}\n`);
    if (row.language_id) io.write(`  更新说明语言：${row.language_id}\n`);
  }
  if ((await ask('输入 yes 保存配置')) !== 'yes') throw new Error('已取消配置，未保存应用配置');
  return project;
}
