import { constants, createPrivateKey, sign as signBytes } from 'node:crypto';
import { type Channel, digest, parseStrictJSON } from '../model.js';
import { type CredentialSet } from '../credentials.js';
import { type Store } from '../state.js';
import { ChannelHTTP, check, num, object, required, str, unknown } from './http.js';

function b64(value: string): string { return Buffer.from(value).toString('base64url'); }

export function huaweiJWT(serviceJSON: string, now = Date.now()): string {
  const service = object(parseStrictJSON(serviceJSON));
  const keyId = str(service.key_id);
  const subAccount = str(service.sub_account);
  const privateKey = str(service.private_key);
  required(keyId, subAccount, privateKey);
  const key = createPrivateKey(privateKey);
  if (key.asymmetricKeyType !== 'rsa' || (key.asymmetricKeyDetails?.modulusLength ?? 0) < 2048) throw new Error('Huawei service account key is invalid');
  const seconds = Math.floor(now / 1000);
  const signed = `${b64(JSON.stringify({ alg: 'PS256', kid: keyId, typ: 'JWT' }))}.${b64(JSON.stringify({
    aud: 'https://oauth-login.cloud.huawei.com/oauth2/v3/token', iss: subAccount, iat: seconds, exp: seconds + 3600 }))}`;
  const signature = signBytes('sha256', Buffer.from(signed), { key, padding: constants.RSA_PKCS1_PSS_PADDING, saltLength: 32 });
  return `${signed}.${signature.toString('base64url')}`;
}

export async function bearer(channel: Channel, account: string, clientId: string, credentials: CredentialSet,
  store: Store, http: ChannelHTTP): Promise<string> {
  if (channel === 'huawei') return huaweiJWT(credentials.service_account_json ?? '');
  if (channel !== 'honor' && channel !== 'oppo') return '';
  const scope = digest([channel, account, clientId, 'token']);
  const existing = store.beginToken(scope);
  if (existing) return existing;
  try {
    let token: string; let expiry: number;
    if (channel === 'honor') {
      const secret = credentials.client_secret ?? '';
      required(clientId, secret);
      const form = Buffer.from(new URLSearchParams({ grant_type: 'client_credentials', client_id: clientId, client_secret: secret }).toString());
      const response = await http.send({ method: 'POST', url: 'https://iam.developer.honor.com/auth/token', class: 'lifecycle',
        headers: { 'Content-Type': 'application/x-www-form-urlencoded; charset=utf-8' }, body: form, size: form.length });
      token = str(response.access_token);
      const seconds = num(response.expires_in);
      if (!token || response.token_type !== 'Bearer' || seconds === null || seconds <= 30 || seconds > 86400) throw unknown();
      expiry = Date.now() + seconds * 1000;
    } else {
      const secret = credentials.client_secret ?? '';
      required(clientId, secret);
      const query = new URLSearchParams({ client_id: clientId, client_secret: secret });
      const response = await http.send({ method: 'GET', path: `/developer/v1/token?${query}`, class: 'lifecycle' });
      check(response, 'errno');
      const data = object(response.data);
      token = str(data.access_token);
      const seconds = num(data.expire_in);
      if (!token || seconds === null || seconds <= Date.now() / 1000 || seconds > 253402300799) throw unknown();
      expiry = seconds * 1000;
    }
    store.finishToken(scope, token, new Date(expiry).toISOString());
    return token;
  } catch (error) {
    store.unknownToken(scope);
    throw error;
  }
}
