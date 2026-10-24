import { type Batch, type FrozenTarget } from '../model.js';
import { type CredentialSet } from '../credentials.js';
import { type Store } from '../state.js';
import { type ChannelAdapter } from './types.js';
import { HuaweiAdapter } from './huawei.js';
import { HonorAdapter } from './honor.js';
import { XiaomiAdapter } from './xiaomi.js';
import { OPPOAdapter } from './oppo.js';
import { VivoAdapter } from './vivo.js';
import { TencentAdapter } from './tencent.js';

export function channelAdapter(batch: Batch, target: FrozenTarget, credentials: CredentialSet,
  store: Store, fetcher: typeof fetch = fetch): ChannelAdapter {
  const args = [batch, target, credentials, store, fetcher] as const;
  switch (target.channel) {
    case 'huawei': return new HuaweiAdapter(...args);
    case 'honor': return new HonorAdapter(...args);
    case 'xiaomi': return new XiaomiAdapter(...args);
    case 'oppo': return new OPPOAdapter(...args);
    case 'vivo': return new VivoAdapter(...args);
    case 'tencent': return new TencentAdapter(...args);
  }
}
