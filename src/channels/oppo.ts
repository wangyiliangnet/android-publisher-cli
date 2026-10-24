import { type Preflight } from '../model.js';
import { md5File } from '../private-file.js';
import { type Observation } from '../state.js';
import { bearer } from './auth.js';
import { BaseAdapter, type StepReceipt } from './types.js';
import { check, decimal, hmacSign, id, object, required, str, unknown } from './http.js';

const metadataFields = ['app_name', 'app_subname', 'second_category_id', 'third_category_id', 'summary',
  'detail_desc', 'update_desc', 'privacy_source_url', 'icon_url', 'pic_url', 'landscape_pic_url',
  'video_url', 'online_type', 'sche_online_time', 'test_desc', 'electronic_cert_url', 'copyright_url',
  'icp_url', 'special_url', 'special_file_url', 'business_username', 'business_email', 'business_mobile',
  'business_qq', 'business_position', 'business_address', 'age_level', 'adaptive_equipment',
  'adaptive_type', 'customer_contact', 'audit_status', 'audit_status_name', 'state', 'apk_md5', 'version_id'] as const;
const requiredFields = ['app_name', 'second_category_id', 'third_category_id', 'summary', 'detail_desc',
  'privacy_source_url', 'icon_url', 'pic_url', 'test_desc', 'business_username', 'business_email',
  'business_mobile', 'age_level', 'adaptive_equipment'] as const;
interface Grant { url: string; sign: string; expires_at: string; }
interface Uploaded { url: string; md5: string; ref: string; size: number; }

export class OPPOAdapter extends BaseAdapter {
  readonly channel = 'oppo' as const;
  readonly steps = ['authorize', 'upload', 'submit'] as const;

  private async signed(fields: Record<string, string>): Promise<Record<string, string>> {
    const clientId = this.target.client_id ?? this.target.account;
    const token = await bearer('oppo', this.target.account, clientId, this.secrets, this.store, this.http);
    const secret = this.secrets.client_secret ?? '';
    required(token, secret);
    const values = { ...fields, access_token: token, timestamp: String(Math.floor(Date.now() / 1000)) };
    return { ...values, api_sign: hmacSign(values, secret) };
  }

  private async query(path: string, fields: Record<string, string>): Promise<Record<string, unknown>> {
    const response = await this.http.send({ method: 'GET', path: `${path}?${new URLSearchParams(await this.signed(fields))}`, class: 'read' });
    check(response, 'errno');
    return response;
  }

  private async details(version = ''): Promise<{ version: string; appId: string; metadata: Record<string, string> }> {
    const fields: Record<string, string> = { pkg_name: this.batch.package_name };
    if (version) fields.version_code = version;
    const response = await this.query('/resource/v1/app/info', fields);
    const data = object(response.data);
    const code = decimal(data.version_code);
    const appId = id(data.app_id);
    if (!code || BigInt(code) <= 0n || data.pkg_name !== this.batch.package_name || !appId || version && code !== version) throw unknown();
    const metadata: Record<string, string> = {};
    for (const key of metadataFields) {
      const value = data[key];
      if (typeof value === 'string') metadata[key] = value;
      else if (typeof value === 'number' || typeof value === 'bigint') metadata[key] = String(value);
    }
    return { version: code, appId, metadata };
  }

  private copyright(metadata: Record<string, string>): string {
    if (metadata.copyright_url) return metadata.copyright_url;
    try { if (new URL(metadata.electronic_cert_url ?? '').protocol === 'https:') return metadata.electronic_cert_url ?? ''; }
    catch { /* no usable existing proof */ }
    return '';
  }

  private updateFields(baseline: Record<string, string>, file: Uploaded): Record<string, string> {
    const fields: Record<string, string> = {};
    for (const key of metadataFields) {
      if (['audit_status', 'audit_status_name', 'state', 'apk_md5', 'version_id'].includes(key)) continue;
      if (baseline[key]) fields[key] = baseline[key];
    }
    fields.update_desc = this.batch.release_notes;
    fields.copyright_url = this.copyright(fields);
    fields.online_type = '1';
    delete fields.sche_online_time;
    for (const field of requiredFields) required(fields[field] ?? '');
    required(fields.update_desc, fields.copyright_url);
    if ([...(fields.summary ?? '')].length > 13 || /[\s\p{P}]/u.test(fields.summary ?? '') ||
      [...(fields.detail_desc ?? '')].length < 20 || [...fields.update_desc].length < 5 ||
      [...(fields.test_desc ?? '')].length > 400 || !['4', '5', '6'].includes(fields.adaptive_equipment ?? '') ||
      ['second_category_id', 'third_category_id', 'age_level'].some(k => !/^[1-9][0-9]*$/.test(fields[k] ?? '')) ||
      (fields.pic_url ?? '').split(',').length < 2) throw new Error('OPPO baseline metadata is incomplete or invalid');
    fields.pkg_name = this.batch.package_name;
    fields.version_code = this.batch.version_code;
    fields.apk_url = JSON.stringify([{ url: file.url, md5: file.md5, cpu_code: 0 }]);
    return fields;
  }

  async preflight(): Promise<Preflight> {
    const details = await this.details();
    if (BigInt(details.version) >= BigInt(this.batch.version_code)) throw new Error('OPPO existing version differs');
    const synthetic: Uploaded = { url: 'https://example.invalid/file', md5: '0'.repeat(32), ref: 'preview', size: 1 };
    this.updateFields(details.metadata, synthetic);
    return this.preflightRecord({ remote_version_code: details.version, remote_ref: details.appId,
      metadata: details.metadata });
  }

  async execute(step: string, operationId: string): Promise<StepReceipt> {
    if (step === 'authorize') {
      const signed = await this.signed({});
      const response = await this.http.send({ method: 'GET', path: `/resource/v1/upload/get-upload-url?${new URLSearchParams(signed)}`,
        class: 'write', operationId });
      check(response, 'errno');
      const data = object(response.data);
      const url = str(data.upload_url);
      const sign = str(data.sign);
      let endpoint: URL;
      try { endpoint = new URL(url); } catch { throw unknown(); }
      const host = endpoint.hostname.toLowerCase();
      if (!sign || endpoint.protocol !== 'https:' || endpoint.username || endpoint.password || endpoint.port || endpoint.hash ||
        !(host === 'api.open.oppomobile.com' || host === 'oop-openapi-cn.heytapmobi.com' || host.endsWith('.heytapmobi.com'))) throw unknown();
      const grant: Grant = { url, sign, expires_at: new Date(Date.now() + 30_000).toISOString() };
      return { ref: `grant-${operationId}`, material: { value: grant, expiresAt: grant.expires_at } };
    }
    if (step === 'upload') {
      const grant = this.material('authorize') as Grant;
      const response = await this.http.multipart(grant.url, { type: 'apk', sign: grant.sign }, { file: this.target.artifact },
        'write', operationId, {}, true);
      check(response, 'errno');
      const data = object(response.data);
      const fileUrl = str(data.url);
      const size = Number(decimal(data.file_size));
      const md5 = md5File(this.target.artifact.path, this.target.artifact.sha256);
      const ref = id(data.id);
      let endpoint: URL;
      try { endpoint = new URL(fileUrl); } catch { throw unknown(); }
      if (!['https:', 'http:'].includes(endpoint.protocol) || !endpoint.host || endpoint.username || endpoint.password || endpoint.hash ||
        size !== this.target.artifact.size || str(data.md5).toLowerCase() !== md5 || data.file_extension !== 'apk' || !ref) throw unknown();
      const uploaded: Uploaded = { url: fileUrl, md5, ref, size };
      return { ref, material: { value: uploaded, expiresAt: '9999-12-31T00:00:00.000Z' } };
    }
    if (step === 'submit') {
      const file = this.material('upload') as Uploaded;
      if (file.ref !== this.ref('upload')) throw new Error('OPPO upload receipt differs from file');
      const baseline = this.savedPreflight().metadata;
      if (!baseline) throw new Error('OPPO baseline missing');
      const fields = this.updateFields(baseline, file);
      const response = await this.http.form('/resource/v1/app/upd', 'write', await this.signed(fields), operationId);
      check(response, 'errno');
      if (object(response.data).success !== true) throw unknown();
      return { ref: `ack-${operationId}` };
    }
    throw new Error('unsupported OPPO step');
  }

  async observe(_remoteRef: string): Promise<Observation> {
    const details = await this.details(this.batch.version_code);
    const file = this.material('upload') as Uploaded;
    if (details.appId !== this.savedPreflight().remote_ref || details.metadata.apk_md5?.toLowerCase() !== file.md5 || !details.metadata.version_id)
      return this.observation('unknown', 'unknown', 'unconfirmed', 'oppo_version_unbound');
    const code = details.metadata.audit_status;
    const review = ['1', '4', '00'].includes(code ?? '') ? 'in_review' :
      ['2', '6', '7', '111'].includes(code ?? '') ? 'approved' :
      ['3', '5', '-11', '444'].includes(code ?? '') ? 'rejected' : 'unknown';
    const availability = code === '111' && details.metadata.state === '1' ? 'live' :
      code === '222' && details.metadata.state === '2' ? 'not_live' : 'unknown';
    return this.observation(review, availability, 'confirmed', 'oppo_version_audit', details.metadata.version_id);
  }
}
