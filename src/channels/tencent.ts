import { type Preflight } from '../model.js';
import { md5File } from '../private-file.js';
import { type Observation } from '../state.js';
import { BaseAdapter, type StepReceipt } from './types.js';
import { validateChannelArtifact } from './capabilities.js';
import { check, decimal, hmacSign, required, str, unknown } from './http.js';

interface Grant { url: string; serial: string; expires_at: string; }

export class TencentAdapter extends BaseAdapter {
  readonly channel = 'tencent' as const;
  readonly steps = ['authorize', 'upload', 'submit'] as const;

  private async call(verb: string, fields: Record<string, string>, operationId?: string): Promise<Record<string, unknown>> {
    const secret = this.secrets.api_secret ?? '';
    const userId = this.target.client_id ?? '';
    required(secret, userId);
    const signed = { ...fields, user_id: userId, timestamp: String(Math.floor(Date.now() / 1000)) };
    const form = { ...signed, sign: hmacSign(signed, secret) };
    const response = await this.http.form(`/open_file/developer_api/${verb}`, operationId ? 'write' : 'read', form, operationId);
    check(response, 'ret');
    return response;
  }

  private async details(): Promise<{ version: string; appName: string; description: string }> {
    const response = await this.call('query_app_detail', { pkg_name: this.batch.package_name, app_id: this.target.app_id });
    if (response.pkg_name !== this.batch.package_name) throw unknown();
    let version = '';
    for (const key of ['VersionCode', 'version_code']) {
      if (!(key in response)) continue;
      const raw = response[key];
      if (!(typeof raw === 'number' && Number.isSafeInteger(raw) || typeof raw === 'bigint')) throw unknown();
      const next = decimal(raw);
      if (!next || BigInt(next) <= 0n || BigInt(next) > 9223372036854775807n || version && version !== next) throw unknown();
      version = next;
    }
    if (!version) throw unknown();
    return { version, appName: str(response.app_name), description: str(response.introduce) };
  }

  async preflight(): Promise<Preflight> {
    const details = await this.details();
    if (BigInt(details.version) >= BigInt(this.batch.version_code)) throw new Error('Tencent target version must exceed the existing version');
    return this.preflightRecord({ remote_version_code: details.version,
      metadata: { app_name: details.appName, description: details.description } });
  }

  async execute(step: string, operationId: string): Promise<StepReceipt> {
    if (step === 'authorize') {
      const response = await this.call('get_file_upload_info', { pkg_name: this.batch.package_name,
        app_id: this.target.app_id, file_type: 'apk', file_name: this.target.artifact.filename }, operationId);
      const url = str(response.pre_sign_url);
      const serial = str(response.serial_number);
      let parsed: URL;
      try { parsed = new URL(url); } catch { throw unknown(); }
      if (parsed.protocol !== 'https:' || !parsed.hostname.endsWith('.myqcloud.com') || !parsed.hostname.includes('.cos.') || !serial) throw unknown();
      const signedTime = parsed.searchParams.get('q-sign-time')?.split(';') ?? [];
      const end = Number(signedTime[1]);
      if (signedTime.length !== 2 || !Number.isSafeInteger(end) || end <= Date.now() / 1000) throw unknown();
      const grant: Grant = { url, serial, expires_at: new Date(end * 1000).toISOString() };
      return { ref: serial, material: { value: grant, expiresAt: grant.expires_at } };
    }
    if (step === 'upload') {
      const grant = this.material('authorize') as Grant;
      if (grant.serial !== this.ref('authorize')) throw new Error('Tencent upload grant does not match its receipt');
      await this.http.upload(grant.url, this.target.artifact, operationId);
      return { ref: grant.serial };
    }
    if (step === 'submit') {
      const serial = this.ref('upload');
      const abis = this.target.artifact.abis;
      validateChannelArtifact(this.channel, this.target.artifact);
      const slot = abis.some(abi => ['armeabi-v7a', 'x86'].includes(abi)) ? '32' : '64';
      const fields: Record<string, string> = { pkg_name: this.batch.package_name, app_id: this.target.app_id,
        feature: this.batch.release_notes, deploy_type: '1', apk32_flag: '2', apk64_flag: '2',
        [`apk${slot}_flag`]: '1', [`apk${slot}_file_serial_number`]: serial,
        [`apk${slot}_file_md5`]: md5File(this.target.artifact.path, this.target.artifact.sha256) };
      await this.call('update_app', fields, operationId);
      return { ref: `ack-${operationId}` };
    }
    throw new Error('unsupported Tencent step');
  }

  async observe(_remoteRef: string): Promise<Observation> {
    const response = await this.call('query_app_update_status', { pkg_name: this.batch.package_name, app_id: this.target.app_id });
    const code = decimal(response.audit_status);
    if (!code) throw unknown();
    const label: Record<string, string> = { '1': 'in_review', '2': 'rejected', '3': 'approved', '8': 'withdrawn' };
    return this.observation('unknown', 'unknown', 'unconfirmed', 'tencent_unversioned_audit', undefined,
      { audit_code: code, audit_status: label[code] ?? 'unknown' });
  }
}
