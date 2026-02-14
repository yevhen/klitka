import { test } from "node:test";
import assert from "node:assert/strict";
import https from "node:https";
import type { AddressInfo } from "node:net";

import { Sandbox } from "../src/index.ts";
import { launchDaemon, shutdownDaemon } from "./helpers.ts";

const TEST_KEY = `-----BEGIN PRIVATE KEY-----
MIIEvAIBADANBgkqhkiG9w0BAQEFAASCBKYwggSiAgEAAoIBAQC8Y2wgo8pSrX5a
ZxkzkeQFch+svThiIGU0Lt7tlRo/1d+dBglMGS/AWMo2bzJ5rdkC0f5+75/NmDfA
O8WR59ZPzkA9WAZOVwVa2/2df/WTlcqD7kf2eRngj0v80Q9Ma7Sg9bKqCB4BCAp5
bmI8kz9LrbegpGPrpiu0WWIXn6slLxVseCrwsUbjxnbONsweiwB/CWqHc8m7v4DE
TvU8PGvgFlLA3FRpJ+tFig8LvhzmDsD2R2JH+JGese1A6tvCrg41tIx96R1dVW+V
RWkScETEgSRDZlrZoYLgrRkDRpnI8dxd+fyWubfjF+6M/POyENsZ3ieCLxSJXTeg
gWGx9/Q7AgMBAAECggEAGEUKug2/0/Tr8UOU+JLT+GuibwOVjvazcwySxbLOxeiM
vVL4wagBAduueWLL8ucVrQpWqg2+3aK9k/NfWZOvhWqB1iVP8qm9U95BhxfkEFZc
17BL7xHc5pQvauuX9/VsOMxObx9KlkFt8ATrfPhPUDWaEYT8JnCq8roNLBPy3YBB
HxJFFzHpAsgGzcZaH1uMRXmfKZx/u9dNUJICX/fxVQ7epCG7x4sw7xZ4vgYiGM+6
fmfdXZx3qQHX+6INdl3BFntsHkRr0RbX9XcDqP5WdYYjLLIex4zzHBfnMBjBNAnn
HqmnpQgXWa03iDVHaPreL7O2Oow4jXPHq6EquzIB+QKBgQD/FYQT1gxN3+rYqCog
p49qY0IiQ1UrFyBL1w0tuZAEAOtIZTGFioSzxZTEbBV4zFZKgTzRYW5ZOw3xwp82
C8mvUr/56JtrGGctYcNkaBEyJk9skTvsAu7EjuaxqSopSf/ue7X2/o0Ay12V3DGS
lKDwd01UPUS4GfwqyBR96oivXQKBgQC9EJjRtzEMpGBBGX3r918r9D4+Eji3MVco
zWE5TkTpQFc6LJkDKjqIyCuhwEmzzvROnIIg6ScP/L8HbOPi1Czx/ysxWm8jSlaP
sTguz7oVI4ccTlcU018B18sYTiLT/DpkPWMXbsOkNbK6xB1ZOUNuPbijiT7NTLZP
HVmQefEwdwKBgDN9d1y9r1wk3/X99AsFZ8i04ouiBdYd4/ILJejd9TkpqlTBwH5R
WLolHwOLQcZRkPWXItytCyZN6mGrxJGXTY3raT8b+gtjMKiTfGqPKzFxVET5CBx6
9xGMOvsPx6fv/Q55wGBsP7AyXOC8QvFwuQ/xNRXVDEMRU7qbCq/kINUtAoGAPa5d
fQXcDbjO4k4zw7kHpqpfaBa/xBxnyBsBHhYH62UfUA5euSacxCUx/uph4TSihccP
uTb3lIKru/tteYIpS6Yo7EgJvCSzituRbcw9dEoL+VMhm9y9wTcqvjo3qJtAXZWd
b3amgzs1nTMANCy3cA7Y3xmWkJn3XGZB4x21b08CgYBmd5raGFlWqjum6ExlhADC
D4EPRIsHn71fEcu8Y1UjQnDcCsHCmCTvOs+TroASzaG8fKrI9zS5BudgdgqLrc28
KoEnhu/2fUMsc1/W4gpeO7kMWntm3ppbKSrASzkz7O2oBRVdZYA6c9MyM6NYX6bp
F+1jiWSdxnQBC3x+NBJ/GA==
-----END PRIVATE KEY-----
`;

const TEST_CERT = `-----BEGIN CERTIFICATE-----
MIIDCTCCAfGgAwIBAgIUCZSuZ+HABuGRUEJpMohquQb/EvQwDQYJKoZIhvcNAQEL
BQAwFDESMBAGA1UEAwwJbG9jYWxob3N0MB4XDTI2MDIwNTE1NDEyMVoXDTI2MDIw
NjE1NDEyMVowFDESMBAGA1UEAwwJbG9jYWxob3N0MIIBIjANBgkqhkiG9w0BAQEF
AAOCAQ8AMIIBCgKCAQEAvGNsIKPKUq1+WmcZM5HkBXIfrL04YiBlNC7e7ZUaP9Xf
nQYJTBkvwFjKNm8yea3ZAtH+fu+fzZg3wDvFkefWT85APVgGTlcFWtv9nX/1k5XK
g+5H9nkZ4I9L/NEPTGu0oPWyqggeAQgKeW5iPJM/S623oKRj66YrtFliF5+rJS8V
bHgq8LFG48Z2zjbMHosAfwlqh3PJu7+AxE71PDxr4BZSwNxUaSfrRYoPC74c5g7A
9kdiR/iRnrHtQOrbwq4ONbSMfekdXVVvlUVpEnBExIEkQ2Za2aGC4K0ZA0aZyPHc
Xfn8lrm34xfujPzzshDbGd4ngi8UiV03oIFhsff0OwIDAQABo1MwUTAdBgNVHQ4E
FgQU/8GEHciuAO+XrqMzoa7hfItEnuAwHwYDVR0jBBgwFoAU/8GEHciuAO+XrqMz
oa7hfItEnuAwDwYDVR0TAQH/BAUwAwEB/zANBgkqhkiG9w0BAQsFAAOCAQEAh3ZK
spgGiQ293CdRZmivaGX3L5vO1e9+ZHXXlkaKXKfEoaVrYeAnww2/NqZBKlps3dso
f8oH7Z91jbL/9eGdcuk5ZxLvhD3fz1HAJEMvTxMoRtQZC3kHqOpPTwX/FJ/nFgQH
Ho2GIoNJtUhnCKJzAm7xRpTZzZUSAJdRgGLnQeUfLqSeNB7L4EIYhbRmBGhgrLRU
HNrFNbpqyh/opfzfVAtxkLIN78ZU7usCXJ05W04tNscuHJ1x0a7YaV1b8KB3N8zQ
zobH1M1KIUcEmq0oMqO2VqegRZVnFfbLYDRmh2fhJ6rNC83Et4ONSx9oJLF3zcI+
YRATWPLem12PXj0yvQ==
-----END CERTIFICATE-----
`;

for (const fsBackend of ["auto", "fsrpc"] as const) {
  test(`sdk secret injection (fs_backend=${fsBackend})`, async () => {
    const { daemon, port } = await launchDaemon({
      KLITKA_PROXY_INSECURE: "1",
      KLITKA_FS_BACKEND: fsBackend,
    });

    let resolveHeader: (value: string) => void = () => undefined;
    const headerPromise = new Promise<string>((resolve) => {
      resolveHeader = resolve;
    });

    const server = https.createServer({ key: TEST_KEY, cert: TEST_CERT }, (req, res) => {
      resolveHeader(String(req.headers["authorization"] ?? ""));
      res.writeHead(200, { "content-type": "text/plain" });
      res.end("ok");
    });

    await new Promise<void>((resolve) => {
      server.listen(0, "127.0.0.1", () => resolve());
    });

    try {
      const address = server.address() as AddressInfo;
      const secret = "super-secret";

      const sandbox = await Sandbox.start({
        baseUrl: `http://127.0.0.1:${port}`,
        network: { allowHosts: ["localhost"], blockPrivateRanges: false },
        secrets: {
          API_KEY: { hosts: ["localhost"], value: secret },
        },
      });

      const targetUrl = `https://localhost:${address.port}`;
      const result = await sandbox.exec([
        "sh",
        "-c",
        `echo $API_KEY; curl -fsS -H "Authorization: Bearer $API_KEY" ${targetUrl}`,
      ]);

      const output = new TextDecoder().decode(result.stdout);
      assert.equal(result.exitCode, 0, `exec failed (code ${result.exitCode}): ${output}`);
      assert.ok(!output.includes(secret), `secret leaked in output: ${output}`);

      const header = await promiseWithTimeout(headerPromise, 5000);
      assert.equal(header, `Bearer ${secret}`);

      await sandbox.close();
    } finally {
      server.close();
      await shutdownDaemon(daemon);
    }
  });
}

function promiseWithTimeout<T>(promise: Promise<T>, timeoutMs: number): Promise<T> {
  return new Promise((resolve, reject) => {
    const timer = setTimeout(() => reject(new Error("timed out waiting for response")), timeoutMs);
    promise
      .then((value) => {
        clearTimeout(timer);
        resolve(value);
      })
      .catch((err) => {
        clearTimeout(timer);
        reject(err);
      });
  });
}
