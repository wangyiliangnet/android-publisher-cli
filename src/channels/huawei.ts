import { type Preflight } from '../model.js';
import { type Observation } from '../state.js';
import { bearer } from './auth.js';
import { BaseAdapter, type StepReceipt } from './types.js';
import { check, decimal, list, object, required, str, unknown } from './http.js';

interface Grant { url: string; ref: string; expires_at: string; headers: Record<string, string>; }

export class HuaweiAdapter extends BaseAdapter {
  readonly channel = 'huawei' as const;
  readonly steps = ['authorize', 'upload', 'bind', 'metadata', 'submit'] as const;

  private async call(path: string, method: 'GET' | 'POST' | 'PUT', kind: 'read' | 'write', value?: unknown, operationId?: string): Promise<Record<string, unknown>> {
    const token = await bearer('huawei', this.target.account, '', this.secrets, this.store, this.http);
    const response = await this.http.json(path, method, kind, value, operationId, { Authorization: `Bearer ${token}` });
    check(object(response.ret), 'code');
    return response;
  }

  private async details(): Promise<{ version: string; metadata: Record<string, string>; versionId: string }> {
    const query = new URLSearchParams({ packageName: this.batch.package_name, packageTypes: '1' });
    const bound = await this.call(`/api/publish/v2/appid-list?${query}`, 'GET', 'read');
    const apps = list(bound.appids);
    if (apps.length !== 1 || apps[0]?.value !== this.target.app_id) throw unknown();
    const response = await this.call(`/api/publish/v2/app-info?${new URLSearchParams({ appId: this.target.app_id })}`, 'GET', 'read');
    const info = object(response.appInfo);
    const version = decimal(info.versionCode);
    if (!version || BigInt(version) <= 0n) throw unknown();
    const languages = list(response.languages);
    let language = this.target.language_id ?? '';
    if (!language) {
      const matches = languages.filter(row => str(row.lang).replaceAll('_', '-').toLowerCase() === 'zh-cn');
      if (matches.length !== 1) throw new Error('Huawei Chinese language is missing or ambiguous');
      language = str(matches[0]?.lang);
    }
    const selected = languages.filter(row => row.lang === language);
    if (selected.length !== 1) throw unknown();
    const row = selected[0]!;
    const metadata = { language_id: language, app_name: str(row.appName), intro: str(row.appDesc),
      brief_intro: str(row.briefInfo), prior_notes: str(row.newFeatures) };
    if (!metadata.app_name || !metadata.intro) throw unknown();
    return { version, metadata, versionId: str(info.versionId) };
  }

  async preflight(): Promise<Preflight> {
    const details = await this.details();
    if (BigInt(details.version) >= BigInt(this.batch.version_code)) throw new Error('Huawei target version must exceed the editable version');
    return this.preflightRecord({ remote_version_code: details.version, remote_ref: details.versionId,
      metadata: details.metadata });
  }

  async waitBeforeStep(step: string): Promise<void> {
    if (step !== 'submit') return;
    const bound = this.store.latestOperation(this.batch.batch_id, 'huawei', 'bind');
    if (!bound || bound.effect !== 'confirmed_applied') throw new Error('Huawei bind receipt missing');
    const remaining = Date.parse(bound.completed_at) + 120_000 - Date.now();
    if (remaining > 0) await new Promise(resolve => setTimeout(resolve, remaining));
  }

  async execute(step: string, operationId: string): Promise<StepReceipt> {
    const appId = this.target.app_id;
    if (step === 'authorize') {
      const started = Date.now();
      const query = new URLSearchParams({ appId, fileName: this.target.artifact.filename,
        sha256: this.target.artifact.sha256, contentLength: String(this.target.artifact.size), releaseType: '1' });
      const response = await this.call(`/api/publish/v2/upload-url/for-obs?${query}`, 'GET', 'write', undefined, operationId);
      const info = object(response.urlInfo);
      const url = str(info.url); const ref = str(info.objectId);
      const method = str(info.method);
      let endpoint: URL;
      try { endpoint = new URL(url); } catch { throw unknown(); }
      const host = endpoint.hostname.toLowerCase();
      if (!ref || method !== 'PUT' || endpoint.protocol !== 'https:' || endpoint.username || endpoint.password || endpoint.port || endpoint.hash ||
        !(host === 'connect-api.cloud.huawei.com' || host === 'nsp-appgallery-agcfs-drcn.obs.cn-north-2.myhuaweicloud.cn' || host.endsWith('.dbankcdn.com'))) throw unknown();
      const headers = object(info.headers);
      const expected = ['Authorization', 'X-Amz-Content-Sha256', 'X-Amz-Date', 'Host', 'User-Agent', 'Content-Type'];
      const values: Record<string, string> = {};
      for (const name of expected) {
        const key = Object.keys(headers).find(k => k.toLowerCase() === name.toLowerCase());
        if (!key || typeof headers[key] !== 'string' || !headers[key] || /[\r\n]/.test(headers[key])) throw unknown();
        values[name] = headers[key];
      }
      if (!values.Authorization?.startsWith('AWS4-HMAC-SHA256 ') && !values.Authorization?.startsWith('OBS ')) throw unknown();
      if (values['X-Amz-Content-Sha256'] !== this.target.artifact.sha256 || values.Host?.toLowerCase() !== endpoint.host.toLowerCase() || values['Content-Type'] !== 'application/octet-stream') throw unknown();
      const grant: Grant = { url, ref, headers: values, expires_at: new Date(started + 4 * 60_000).toISOString() };
      return { ref, material: { value: grant, expiresAt: grant.expires_at } };
    }
    if (step === 'upload') {
      const grant = this.material('authorize') as Grant;
      if (grant.ref !== this.ref('authorize')) throw new Error('Huawei upload grant differs from authorization receipt');
      const { Host: _host, ...headers } = grant.headers;
      await this.http.upload(grant.url, this.target.artifact, operationId, headers);
      return { ref: grant.ref };
    }
    if (step === 'bind') {
      const ref = this.ref('upload');
      await this.call(`/api/publish/v2/app-file-info?${new URLSearchParams({ appId })}`, 'PUT', 'write',
        { fileType: 5, files: [{ fileName: this.target.artifact.filename, fileDestUrl: ref }] }, operationId);
      return { ref };
    }
    if (step === 'metadata') {
      const baseline = this.savedPreflight().metadata;
      if (!baseline) throw new Error('Huawei language baseline missing');
      required(baseline.language_id ?? '', baseline.app_name ?? '', baseline.intro ?? '', this.batch.release_notes);
      if ([...this.batch.release_notes].length > 500) throw new Error('Huawei notes exceed 500 characters');
      await this.call(`/api/publish/v2/app-language-info?${new URLSearchParams({ appId, releaseType: '1' })}`,
        'PUT', 'write', { lang: baseline.language_id, appName: baseline.app_name, appDesc: baseline.intro,
          briefInfo: baseline.brief_intro ?? '', newFeatures: this.batch.release_notes }, operationId);
      return { ref: `ack-${operationId}` };
    }
    if (step === 'submit') {
      await this.call(`/api/publish/v2/app-submit?${new URLSearchParams({ appId, releaseType: '1' })}`, 'POST', 'write', undefined, operationId);
      return { ref: `ack-${operationId}` };
    }
    throw new Error('unsupported Huawei step');
  }

  async observe(_remoteRef: string): Promise<Observation> {
    const response = await this.call(`/api/publish/v2/app-info?${new URLSearchParams({ appId: this.target.app_id })}`, 'GET', 'read');
    const app = object(response.appInfo);
    const version = decimal(app.versionCode);
    const onShelf = decimal(app.onShelfVersionCode);
    if (version !== this.batch.version_code) return this.observation('unknown', 'unknown', 'unconfirmed', 'huawei_app_info', undefined,
      { observed_version_code: version });
    const state = decimal(app.releaseState);
    const review = state === '0' ? 'approved' : ['1', '8', '13'].includes(state) ? 'rejected' : ['4', '5', '12'].includes(state) ? 'in_review' : 'unknown';
    return this.observation(review, onShelf === this.batch.version_code ? 'live' : 'unknown', 'confirmed', 'huawei_target_version', str(app.versionId));
  }
}
