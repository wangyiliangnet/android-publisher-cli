import { type Preflight, digest } from '../model.js';
import { md5File } from '../private-file.js';
import { type Observation } from '../state.js';
import { BaseAdapter, type StepReceipt } from './types.js';
import { validateReleaseNotes } from './capabilities.js';
import { check, decimal, hmacSign, object, required, str, unknown } from './http.js';

const metadataFields = ['onlineType', 'updateDesc', 'detailDesc', 'icon', 'screenshot', 'mainTitle', 'subTitle',
  'appClassify', 'subAppClassify', 'remark', 'specialQualifications', 'ecopyright', 'copyrightList',
  'copyrightAuthLetter', 'safetyreport', 'networkCultureLicense', 'privateSelfCheck', 'compatibleDevice',
  'customerService', 'video', 'videoCover', 'simpleDesc', 'commitmentLetter', 'icpRecordType',
  'icpLicenseType', 'icpLicenseNo', 'icpAuthLetter', 'offInternetLetter', 'rateAge', 'status', 'saleStatus'] as const;
const attachmentFields = new Set(['icon', 'screenshot', 'specialQualifications', 'ecopyright', 'copyrightList',
  'copyrightAuthLetter', 'safetyreport', 'video', 'videoCover', 'commitmentLetter', 'icpAuthLetter', 'offInternetLetter']);
interface Uploaded { serial: string; package_name: string; version_code: string; version_name: string; md5: string; }

export class VivoAdapter extends BaseAdapter {
  readonly channel = 'vivo' as const;
  readonly steps = ['upload', 'submit'] as const;

  private async pace(): Promise<void> {
    const pause = this.store.reservePacing(digest(['vivo', this.target.account]), 60_000);
    if (pause > 0) await new Promise(resolve => setTimeout(resolve, pause));
  }
  private signed(method: string, fields: Record<string, string>): Record<string, string> {
    const secret = this.secrets.access_secret ?? '';
    const access = this.target.client_id ?? '';
    required(secret, access);
    const values = { ...fields, method, access_key: access, timestamp: String(Date.now()),
      format: 'json', v: '1.0', sign_method: 'HMAC-SHA256', target_app_key: 'developer' };
    return { ...values, sign: hmacSign(values, secret, ['sign', 'file']) };
  }
  private validate(response: Record<string, unknown>): void {
    check(response, 'code');
    if (response.subCode !== '0') throw unknown();
  }
  private async details(): Promise<{ version: string; name: string; metadata: Record<string, string> }> {
    await this.pace();
    const response = await this.http.form('/router/rest', 'read', this.signed('app.query.details', { packageName: this.batch.package_name }));
    this.validate(response);
    const data = object(response.data);
    const version = decimal(data.versionCode);
    if (!version || data.packageName !== this.batch.package_name) throw unknown();
    const metadata: Record<string, string> = {};
    for (const key of metadataFields) {
      const value = data[key];
      if (typeof value === 'string') metadata[key] = value;
      else if (typeof value === 'number' || typeof value === 'bigint') metadata[key] = String(value);
    }
    return { version, name: str(data.versionName), metadata };
  }

  async preflight(): Promise<Preflight> {
    validateReleaseNotes(this.batch.release_notes, [this.channel]);
    const details = await this.details();
    if (BigInt(details.version) >= BigInt(this.batch.version_code)) throw new Error('vivo target version must exceed the existing version');
    if (!['1', '2', '3'].includes(details.metadata.compatibleDevice ?? '') ||
      !(details.metadata.ecopyright || details.metadata.copyrightList)) throw new Error('vivo baseline is missing required device or copyright fields');
    if ([...(details.metadata.detailDesc ?? '')].length < 50 ||
      [...(details.metadata.detailDesc ?? '')].length > 1000) throw new Error('vivo release metadata does not meet publishing requirements');
    return this.preflightRecord({ remote_version_code: details.version, metadata: details.metadata });
  }

  async execute(step: string, operationId: string): Promise<StepReceipt> {
    if (step === 'upload') {
      await this.pace();
      const file = this.target.artifact;
      const md5 = md5File(file.path, file.sha256);
      const fields = this.signed('app.upload.apk.app', { packageName: this.batch.package_name, fileMd5: md5 });
      const response = await this.http.multipart('/router/rest', fields, { file }, 'write', operationId);
      this.validate(response);
      const data = object(response.data);
      const version = decimal(data.versionCode);
      const serial = str(data.serialnumber);
      if (data.packageName !== this.batch.package_name || version !== this.batch.version_code ||
        data.versionName !== this.batch.version_name || !serial || str(data.fileMd5).toLowerCase() !== md5) throw unknown();
      const uploaded: Uploaded = { serial, package_name: this.batch.package_name, version_code: version,
        version_name: this.batch.version_name, md5 };
      return { ref: serial, material: { value: uploaded, expiresAt: '9999-12-31T00:00:00.000Z' } };
    }
    if (step === 'submit') {
      const uploaded = this.material('upload') as Uploaded;
      if (uploaded.serial !== this.ref('upload') || uploaded.version_code !== this.batch.version_code) throw new Error('vivo upload receipt differs from target');
      const baseline = this.savedPreflight().metadata;
      if (!baseline) throw new Error('vivo baseline missing');
      const fields: Record<string, string> = {};
      for (const key of metadataFields) if (!attachmentFields.has(key) && key !== 'status' && key !== 'saleStatus' && baseline[key] !== undefined) fields[key] = baseline[key];
      const notes = this.batch.release_notes;
      validateReleaseNotes(notes, [this.channel]);
      if ([...(fields.detailDesc ?? '')].length < 50 || [...(fields.detailDesc ?? '')].length > 1000 ||
        !['1', '2', '3'].includes(fields.compatibleDevice ?? '')) throw new Error('vivo metadata does not meet publishing requirements');
      fields.updateDesc = notes;
      fields.packageName = this.batch.package_name;
      fields.versionCode = this.batch.version_code;
      fields.apk = uploaded.serial;
      fields.fileMd5 = uploaded.md5;
      fields.onlineType = '1';
      await this.pace();
      const response = await this.http.form('/router/rest', 'write', this.signed('app.sync.update.app', fields), operationId);
      this.validate(response);
      return { ref: `ack-${operationId}` };
    }
    throw new Error('unsupported vivo step');
  }

  async observe(_remoteRef: string): Promise<Observation> {
    const details = await this.details();
    if (details.version !== this.batch.version_code) return this.observation('unknown', 'unknown', 'unconfirmed', 'vivo_details', undefined,
      { observed_version_code: details.version });
    const code = details.metadata.status;
    const review = code === '2' ? 'in_review' : code === '3' ? 'approved' : code === '4' ? 'rejected' : 'unknown';
    const availability = details.metadata.saleStatus === '1' && review === 'approved' ? 'live' :
      ['0', '2'].includes(details.metadata.saleStatus ?? '') ? 'not_live' : 'unknown';
    return this.observation(review, availability, 'confirmed', 'vivo_target_version');
  }
}
