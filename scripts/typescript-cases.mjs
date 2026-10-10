// Compare the public TypeScript wrapper with the Go host using deterministic test inputs.
import { resolve, join } from 'node:path';
import { pathToFileURL } from 'node:url';
import { createHash } from 'node:crypto';
const root = resolve(process.env.SDK_TYPESCRIPT_DIR);
const sdk = await import(pathToFileURL(join(root, 'build/test/dist/node/index.js')));
const profile = sdk.profiles.devnet({relays: ["http://127.0.0.1:4003/api"]});
const phrase = 'abandon '.repeat(23) + 'art';
for (let i=0; i<1000; i++) {
  const passphrase = `cross-language ${i} café`;
  const key = sdk.Keys.fromLegacyPassphrase(passphrase,profile);
  const message = `message ${i} 冰根`;
  const aux = createHash('sha256').update(String(i)).digest();
  const signed = sdk.testing.signMessageWithAux(key,message,aux);
  console.log(JSON.stringify({kind:'legacy',passphrase,message,aux:aux.toString('hex'),address:key.address,publicKey:key.publicKey,signed:{publicKey:key.publicKey,signature:signed,algorithm:"secp256k1-bip340-sha256",network:"heartwood-devnet-v90"}}));
  key.release();
  const derived = sdk.Keys.fromPhrase(phrase,profile,{account:i%4,index:i,passphrase});
  console.log(JSON.stringify({kind:'phrase',phrase,account:i%4,index:i,passphrase,address:derived.address,publicKey:derived.publicKey,path:derived.path}));
  derived.release();
}
