import { createHash, createPublicKey, publicEncrypt, constants, X509Certificate } from 'node:crypto';
import path from 'node:path';
import { type Preflight, parseStrictJSON } from '../model.js';
import { hashFile, md5File } from '../private-file.js';
import { type Observation } from '../state.js';
import { BaseAdapter, type StepReceipt } from './types.js';
import { check, decimal, object, required, str, unknown, type UploadFile } from './http.js';

function xiaomiSIG(fields: Record<string, string>, files: Record<string, UploadFile>, password: string, keyPEM: string): string {
  required(password, keyPEM);
  let key;
  try { key = keyPEM.includes('BEGIN CERTIFICATE') ? new X509Certificate(keyPEM).publicKey : createPublicKey(keyPEM); }
  catch { throw new Error('invalid Xiaomi public key'); }
  if (key.asymmetricKeyType !== 'rsa' || (key.asymmetricKeyDetails?.modulusLength ?? 0) < 1024) throw new Error('invalid Xiaomi public key');
  const sig = Object.entries(fields).map(([name, value]) => ({ name, hash: createHash('md5').update(value).digest('hex') }));
  for (const [name, file] of Object.entries(files)) sig.push({ name, hash: md5File(file.path, file.sha256) });
  sig.sort((a, b) => a.name.localeCompare(b.name));
  const plaintext = Buffer.from(JSON.stringify({ sig, password }));
  const keyBytes = Math.ceil((key.asymmetricKeyDetails?.modulusLength ?? 0) / 8);
  const chunks: Buffer[] = [];
  for (let pos = 0; pos < plaintext.length; pos += keyBytes - 11)
    chunks.push(publicEncrypt({ key, padding: constants.RSA_PKCS1_PADDING }, plaintext.subarray(pos, pos + keyBytes - 11)));
  plaintext.fill(0);
  return Buffer.concat(chunks).toString('hex');
}

function validTestAccounts(source: string): void {
  const parsed = object(parseStrictJSON(source));
  const zh = object(parsed.zh_CN);
  if (!Array.isArray(zh.accounts) || zh.accounts.length > 5 || typeof zh.auditNotes !== 'string' || [...zh.auditNotes].length > 500) throw new Error('Xiaomi test accounts are invalid');
  for (const item of zh.accounts) {
    const row = object(item);
    if (![1, 2].includes(row.t as number) || typeof row.a !== 'string' || typeof row.p !== 'string' || typeof row.c !== 'string' ||
      Boolean(row.a) !== Boolean(row.p) || [row.a, row.p, row.c].some(value => [...value].length > 50)) throw new Error('Xiaomi test accounts are invalid');
  }
}

export class XiaomiAdapter extends BaseAdapter {
  readonly channel = 'xiaomi' as const;
  readonly steps = ['push'] as const;

  private async call(verb: 'query' | 'push', data: unknown, files: Record<string, UploadFile> = {}, operationId?: string): Promise<Record<string, unknown>> {
    const requestData = JSON.stringify(data);
    const fields = { RequestData: requestData };
    const sig = xiaomiSIG(fields, files, this.secrets.api_secret ?? '', this.secrets.public_key_pem ?? '');
    const response = await this.http.multipart(`/devupload/dev/${verb}`, { ...fields, SIG: sig }, files,
      verb === 'push' ? 'write' : 'read', operationId);
    check(response, 'result');
    return response;
  }

  private async details(): Promise<{ version: string; appName: string; updateAllowed: boolean }> {
    const response = await this.call('query', { packageName: this.batch.package_name, userName: this.target.client_id });
    const info = object(response.packageInfo);
    const version = decimal(info.versionCode);
    if (!version || info.packageName !== this.batch.package_name || typeof response.updateVersion !== 'boolean') throw unknown();
    return { version, appName: str(info.appName), updateAllowed: response.updateVersion };
  }

  async preflight(): Promise<Preflight> {
    if (!this.target.client_id || !this.target.icon_path || !this.target.icon_sha256 || !this.target.privacy_url) throw new Error('Xiaomi public configuration is incomplete');
    const details = await this.details();
    if (!details.updateAllowed || BigInt(details.version) >= BigInt(this.batch.version_code) || !details.appName) throw new Error('Xiaomi existing application or update permission is not verified');
    const icon = hashFile(this.target.icon_path);
    if (icon.sha256 !== this.target.icon_sha256 || icon.size !== this.target.icon_size) throw new Error('Xiaomi icon changed');
    if (this.secrets.test_account_json) validTestAccounts(this.secrets.test_account_json);
    return this.preflightRecord({ remote_version_code: details.version, metadata: { app_name: details.appName, privacy_url: this.target.privacy_url } });
  }

  async waitBeforeStep(step: string): Promise<void> {
    if (step === 'push' && this.secrets.test_account_json) validTestAccounts(this.secrets.test_account_json);
  }

  async execute(step: string, operationId: string): Promise<StepReceipt> {
    if (step !== 'push') throw new Error('unsupported Xiaomi step');
    const preflight = this.savedPreflight();
    const appName = preflight.metadata?.app_name ?? '';
    const icon: UploadFile = { path: this.target.icon_path!, filename: path.basename(this.target.icon_path!),
      sha256: this.target.icon_sha256!, size: this.target.icon_size! };
    required(appName, this.target.privacy_url ?? '', this.batch.release_notes);
    const info: Record<string, unknown> = { packageName: this.batch.package_name, appName, privacyUrl: this.target.privacy_url,
      updateDesc: this.batch.release_notes, versionName: this.batch.version_name };
    if (this.secrets.test_account_json) {
      validTestAccounts(this.secrets.test_account_json);
      info.testAccount = this.secrets.test_account_json;
    }
    await this.call('push', { userName: this.target.client_id, synchroType: 1, appInfo: info },
    { apk: this.target.artifact, icon }, operationId);
    return { ref: `ack-${operationId}` };
  }

  async observe(_remoteRef: string): Promise<Observation> {
    const details = await this.details();
    return this.observation('unknown', 'unknown', 'unconfirmed', 'xiaomi_package_query', undefined,
      { version_code: details.version, inference: BigInt(details.version) === BigInt(this.batch.version_code) ? 'target_version_returned' : 'version_differs' });
  }
}
