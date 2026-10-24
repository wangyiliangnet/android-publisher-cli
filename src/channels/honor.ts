import { type Preflight } from '../model.js';
import { type Observation } from '../state.js';
import { bearer } from './auth.js';
import { BaseAdapter, type StepReceipt } from './types.js';
import { check, decimal, id, list, object, required, str, unknown } from './http.js';

interface Grant { url: string; ref: string; expires_at: string; }
const integerID = (value: string): boolean => /^[1-9][0-9]{0,18}$/.test(value) && BigInt(value) <= 9223372036854775807n;

export class HonorAdapter extends BaseAdapter {
  readonly channel = 'honor' as const;
  readonly steps = ['authorize', 'upload', 'bind', 'metadata', 'submit'] as const;

  private async call(path: string, method: 'GET' | 'POST', kind: 'read' | 'write', value?: unknown, operationId?: string): Promise<Record<string, unknown>> {
    const token = await bearer('honor', this.target.account, this.target.client_id ?? '', this.secrets, this.store, this.http);
    const response = await this.http.json(path, method, kind, value, operationId, { Authorization: `Bearer ${token}` });
    check(response, 'code');
    return response;
  }

  private async raw(path: string, source: string, kind: 'read' | 'write', operationId?: string): Promise<Record<string, unknown>> {
    const token = await bearer('honor', this.target.account, this.target.client_id ?? '', this.secrets, this.store, this.http);
    const response = await this.http.rawJSON(path, 'POST', kind, source, operationId, { Authorization: `Bearer ${token}` });
    check(response, 'code');
    return response;
  }

  private async details(): Promise<{ version: string; metadata: Record<string, string> }> {
    const pkg = this.batch.package_name;
    const found = await this.call(`/openapi/v1/publish/get-app-id?${new URLSearchParams({ pkgName: pkg })}`, 'GET', 'read');
    const rows = list(found.data);
    if (rows.length !== 1 || rows[0]?.packageName !== pkg || id(rows[0]?.appId) !== this.target.app_id || !integerID(this.target.app_id)) throw unknown();
    const response = await this.call(`/openapi/v1/publish/get-app-detail?${new URLSearchParams({ appId: this.target.app_id })}`, 'GET', 'read');
    const data = object(response.data);
    const basic = object(data.basicInfo);
    if (id(basic.appId) !== this.target.app_id || basic.packageName !== pkg) throw unknown();
    const release = object(data.releaseInfo);
    const version = decimal(release.versionCode);
    if (!version || BigInt(version) <= 0n) throw unknown();
    const languageRows = list(data.languageInfo);
    let language = this.target.language_id ?? '';
    if (!language) {
      const matches = languageRows.filter(row => str(row.languageId).replaceAll('_', '-').toLowerCase() === 'zh-cn');
      if (matches.length !== 1) throw new Error('Honor Chinese language is missing or ambiguous');
      language = str(matches[0]?.languageId);
    }
    const row = languageRows.find(item => item.languageId === language);
    if (!row) throw unknown();
    const metadata = { language_id: language, app_name: str(row.appName), intro: str(row.intro),
      brief_intro: str(row.briefIntro), prior_notes: str(row.newFeature) };
    if (!metadata.app_name || !metadata.intro || !metadata.brief_intro) throw unknown();
    return { version, metadata };
  }

  async preflight(): Promise<Preflight> {
    const details = await this.details();
    if (BigInt(details.version) >= BigInt(this.batch.version_code)) throw new Error('Honor target version must exceed the live version');
    return this.preflightRecord({ remote_version_code: details.version, remote_ref: this.target.app_id, metadata: details.metadata });
  }

  async execute(step: string, operationId: string): Promise<StepReceipt> {
    if (step === 'authorize') {
      const response = await this.call(`/openapi/v1/publish/get-file-upload-url?${new URLSearchParams({ appId: this.target.app_id })}`,
        'POST', 'write', [{ fileName: this.target.artifact.filename, fileType: 100,
          fileSize: this.target.artifact.size, fileSha256: this.target.artifact.sha256 }], operationId);
      const rows = list(response.data);
      if (rows.length !== 1 || rows[0]?.fileName !== this.target.artifact.filename) throw unknown();
      const entry = rows[0]!;
      const ref = id(entry.objectId);
      const expiry = Number(decimal(entry.expireTime));
      const url = str(entry.uploadUrl);
      let endpoint: URL;
      try { endpoint = new URL(url); } catch { throw unknown(); }
      if (!integerID(ref) || !Number.isSafeInteger(expiry) || expiry <= Date.now() / 1000 ||
        endpoint.origin !== 'https://appmarket-openapi-drcn.cloud.honor.com' || endpoint.pathname !== '/openapi/v1/publish/file-upload' ||
        endpoint.searchParams.size !== 2 || endpoint.searchParams.get('appId') !== this.target.app_id || endpoint.searchParams.get('objectId') !== ref) throw unknown();
      const grant: Grant = { url, ref, expires_at: new Date(expiry * 1000).toISOString() };
      return { ref, material: { value: grant, expiresAt: grant.expires_at } };
    }
    if (step === 'upload') {
      const grant = this.material('authorize') as Grant;
      if (grant.ref !== this.ref('authorize')) throw new Error('Honor upload grant differs from authorization receipt');
      const token = await bearer('honor', this.target.account, this.target.client_id ?? '', this.secrets, this.store, this.http);
      const response = await this.http.multipart(grant.url, {}, { file: this.target.artifact }, 'write', operationId,
        { Authorization: `Bearer ${token}` }, true);
      check(response, 'code');
      return { ref: grant.ref };
    }
    if (step === 'bind') {
      const ref = this.ref('upload');
      if (!integerID(ref)) throw new Error('Honor object ID is invalid');
      await this.raw(`/openapi/v1/publish/update-file-info?${new URLSearchParams({ appId: this.target.app_id })}`,
        `{"bindingFileList":[{"objectId":${ref}}]}`, 'write', operationId);
      return { ref };
    }
    if (step === 'metadata') {
      const baseline = this.savedPreflight().metadata;
      if (!baseline) throw new Error('Honor language baseline missing');
      required(baseline.language_id ?? '', baseline.app_name ?? '', baseline.intro ?? '', baseline.brief_intro ?? '', this.batch.release_notes);
      await this.call(`/openapi/v1/publish/update-language-info?${new URLSearchParams({ appId: this.target.app_id })}`,
        'POST', 'write', { setAll: 0, languageInfoList: [{ languageId: baseline.language_id, appName: baseline.app_name,
          intro: baseline.intro, briefIntro: baseline.brief_intro, newFeature: this.batch.release_notes }] }, operationId);
      return { ref: `ack-${operationId}` };
    }
    if (step === 'submit') {
      const response = await this.call(`/openapi/v1/publish/submit-audit?${new URLSearchParams({ appId: this.target.app_id })}`,
        'POST', 'write', { releaseType: 1 }, operationId);
      const ref = str(response.data);
      if (!ref) throw unknown();
      return { ref };
    }
    throw new Error('unsupported Honor step');
  }

  async observe(remoteRef: string): Promise<Observation> {
    if (!integerID(this.target.app_id) || !remoteRef || remoteRef.startsWith('ack-')) return this.observation('unknown', 'unknown', 'unconfirmed', 'honor_audit_missing_release_id');
    const response = await this.raw('/openapi/v1/publish/get-audit-result',
      `{"appId":[{"appId":${this.target.app_id},"releaseId":${JSON.stringify(remoteRef)}}]}`, 'read');
    const rows = list(response.data);
    if (rows.length !== 1 || id(rows[0]?.appId) !== this.target.app_id || rows[0]?.releaseId !== remoteRef) throw unknown();
    const code = decimal(rows[0]?.auditResult);
    const review = code === '0' ? 'in_review' : code === '1' ? 'approved' : code === '2' ? 'rejected' : 'unknown';
    return this.observation(review, 'unknown', 'confirmed', 'honor_release_audit', remoteRef);
  }
}
