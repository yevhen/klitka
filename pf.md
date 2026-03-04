# macOS strict transport enforcement + DNS profiles + full eval matrix

## 1) Цель

Реализовать **transport-level strict egress enforcement** для macOS в `klitka`, чтобы недоверенный код в guest не мог обойти host policy через:

- unset `HTTP_PROXY`/`HTTPS_PROXY`,
- direct TCP/UDP соединения,
- DNS-эксфильтрацию.

При этом сохранить текущую модель:

- allow/deny hosts,
- block private ranges,
- secret injection (MITM),
- DNS profiles (`open|trusted|synthetic`).

---

## 2) Scope

- Платформа: **macOS**
- Режим: `network.egress_mode=strict`
- Механизм PoC: **pf-based transport enforcement** (без собственного TCP/IP стека)

---

## 3) Non-goals

- Не разрабатывать новый userspace network stack.
- Не делать полный redesign Linux path в этом ТЗ.
- Не вводить JS callback policy engine.
- Не делать snapshots/ssh в рамках этого ТЗ.
- Не делать transparent 80/443 redirect в первой итерации (можно отдельным follow-up, если понадобится).

---

## 4) Функциональные требования (FR)

### FR-1. Strict mode activation

При `network.egress_mode = strict` на macOS daemon обязан:

1. создать per-VM pf anchor/rules,
2. активировать правила до запуска workload-трафика,
3. в случае ошибки pf setup — вернуть ошибку StartVM (без fallback в compat).

### FR-2. Direct egress denial

Strict должен блокировать direct outbound из guest (TCP/UDP), кроме явно разрешённых служебных направлений (proxy, DNS path).

### FR-3. Controlled DNS path

В strict DNS-трафик guest должен быть принудительно направлен в host-controlled DNS handling.
Поведение должно соответствовать `dns_mode`:

- `open` — compat resolver path,
- `trusted` — только `trusted_dns_servers`,
- `synthetic` — без внешних DNS lookup.

### FR-4. Lifecycle cleanup

На StopVM и на любом error-path:

- удалить per-VM pf anchor/rules,
- не оставлять “залипших” правил.

### FR-5. Observability

Structured logs + counters:

- `network_enforcement` (`compat|strict`)
- `dns_mode`
- `blocked_egress`
- `dns_blocked`
- VM/anchor идентификатор для correlation.

### FR-6. Compatibility

- `compat` не должен деградировать.
- CLI/SDK DX остаётся backward-compatible.

---

## 5) Нефункциональные требования (безопасность/надёжность)

1. Изоляция правил по VM ID (никаких shared глобальных правил без namespace).
2. Без cross-VM утечек policy.
3. Идемпотентность cleanup (повторный stop/rollback не должен падать).
4. Устойчивость к частичным сбоям daemon/startup.

---

## 6) Инкрементальный итеративный план (Ralph loop) — атомарные E2E slices

> Принцип: каждый slice — завершённый вертикальный инкремент (код + тесты + проверка), без откладывания тестов на финал.
> 
> Каждая итерация: `Implement -> Targeted tests -> Regression smoke -> Commit-ready`.

### Slice 0 — Baseline harness и тестовые фикстуры strict/dns (подготовительный, но с ранней проверкой)

**Цель:** подготовить стабильный тестовый каркас для следующих slices, чтобы все проверки запускались инкрементально.

**Изменения:**

- Ввести общие test helpers для:
  - запуска VM в `strict`/`compat`,
  - фикстур policy (allow/deny/private-range),
  - фикстур DNS modes (`open|trusted|synthetic`),
  - инспекции daemon structured logs/counters.
- Добавить флаги/хуки fault injection для startup/cleanup сценариев (для E1/E3).

**Acceptance:**

- Можно запускать targeted strict/dns suites отдельно (`-count=1`).
- Есть повторяемые e2e-хелперы для Go + SDK integration.

**Проверка после slice:**

- `nix develop --command go test ./... -count=1`
- `nix develop --command bash -lc 'cd sdk && npm test'` (smoke, можно ограничить suite)

---

### Slice 1 — Strict lifecycle enforcement (pf manager + fail-closed activation + cleanup base)

**Связь с PR:** PR-A (первая часть)

**Цель:** реализовать каркас strict lifecycle и fail-closed поведение.

**Изменения:**

- Компонент управления pf (`create/apply/remove/list`) с per-VM anchor namespace.
- Wiring в lifecycle:
  - StartVM: apply pf до workload traffic,
  - StopVM/error-path: обязательный cleanup.
- На ошибке pf setup: жёсткий StartVM error (без fallback в compat).
- Structured logs strict activation/deactivation (`network_enforcement`, `vm_id`, `anchor_id`).

**Acceptance:**

- FR-1 выполнен.
- Базовая часть FR-4 выполнена.
- Нет silent fallback strict -> compat.

**Тесты slice 1 (полные спецификации):**

#### A1. Strict bypass denied (negative)
- **Threat:** guest unsets proxy env and goes direct outbound
- **Test:** inside guest run with unset `HTTP_PROXY` `HTTPS_PROXY` and direct curl
- **Expected:** request fails due to strict transport block
- **Where:** Go e2e + SDK integration

#### E1. Partial startup rollback (negative)
- **Test:** inject failure after pf apply but before full VM ready
- **Expected:** rules removed
- **Where:** daemon unit/integration

#### E2. Stop cleanup idempotence
- **Test:** stop called multiple times / cleanup on already-removed rules
- **Expected:** no crash, clean state
- **Where:** daemon unit

#### H1. Strict startup log fields
- **Expected logs:** `network_enforcement=strict`, `dns_mode=...`, VM/anchor id
- **Where:** daemon log assertions (unit/integration)

**Проверка после slice:**

- Targeted: strict lifecycle + rollback + logs
- `nix develop --command go test ./... -count=1`
- `nix develop --command bash -lc 'cd sdk && npm test'` (suite для A1)

---

### Slice 2 — Policy correctness under strict (allowed path + deny semantics)

**Связь с PR:** PR-A (вторая часть)

**Цель:** довести policy behavior до корректного end-to-end в strict.

**Изменения:**

- Разрешённый policy path для allowlisted хостов (без false positives).
- Чёткая, документированная и протестированная precedence (`deny` vs `allow`).
- Strict enforcement для private/internal targets.

**Acceptance:**

- FR-2 (policy-level behavior) расширен.
- FR-6 (compat не деградирует) подтверждён smoke-регрессией.

**Тесты slice 2 (полные спецификации):**

#### A2. Strict allowed path works (positive)
- **Threat:** false positives, breaking valid traffic
- **Test:** allowlisted host via normal policy path
- **Expected:** success
- **Where:** Go e2e + SDK integration

#### C1. deny over allow precedence (negative)
- **Test:** same host in allow+deny
- **Expected:** deny wins (or documented precedence, consistently tested)
- **Where:** Go e2e/daemon test

#### C2. private range block enforcement (negative)
- **Test:** allow host resolving to private range with block enabled
- **Expected:** blocked
- **Where:** existing + strict variant

#### C3. loopback/internal metadata targets (negative)
- **Test:** access local/internal ranges from guest
- **Expected:** blocked under strict policy
- **Where:** Go e2e

**Проверка после slice:**

- Targeted: policy strict suites + compat smoke
- `nix develop --command go test ./... -count=1`
- `nix develop --command bash -lc 'cd sdk && npm test'`

---

### Slice 3 — Strict DNS forced path + DNS profiles + validation

**Связь с PR:** PR-B

**Цель:** полностью закрыть controlled DNS path в strict для `open|trusted|synthetic`.

**Изменения:**

- Принудительный DNS route через host-controlled path в strict.
- Полная интеграция `dns_mode`:
  - `open`: compat resolver behavior,
  - `trusted`: только `trusted_dns_servers`,
  - `synthetic`: без внешнего lookup.
- Валидация trusted DNS конфигов (empty/invalid endpoints -> hard fail).

**Acceptance:**

- FR-3 выполнен полностью.
- Ошибки конфигурации trusted deterministic и fail-fast.

**Тесты slice 3 (полные спецификации):**

#### B1. Synthetic blocks DNS exfil (negative)
- **Threat:** data-in-subdomain exfil
- **Test:** resolve `<payload>.attacker.test` under synthetic
- **Expected:** blocked/controlled (no external resolver hit)
- **Where:** Go e2e + SDK

#### B2. Trusted uses only configured resolvers (positive/negative)
- **Test:** trusted mode with local controlled resolver
- **Expected:** queries reach only trusted resolver
- **And:** untrusted resolver usage fails
- **Where:** Go e2e + SDK

#### B3. Open mode compatibility (positive)
- **Test:** open mode domain resolution baseline
- **Expected:** compat behavior preserved
- **Where:** Go e2e (at least one smoke)

#### B4. Trusted misconfig hard fail (negative)
- **Test:** trusted mode with empty trusted_dns_servers
- **Expected:** Start/validation error
- **Where:** daemon unit + CLI/SDK integration sanity

#### B5. Invalid trusted DNS endpoint (negative)
- **Test:** invalid IP/port formats
- **Expected:** validation error
- **Where:** daemon unit + CLI parsing test

**Проверка после slice:**

- Targeted: dns strict suites
- `nix develop --command go test ./... -count=1`
- `nix develop --command bash -lc 'cd sdk && npm test'`

---

### Slice 4 — Transport adversarial hardening (non-HTTP protocols, IPv6, DoH/DoT semantics)

**Связь с PR:** PR-C (первая часть)

**Цель:** закрыть обходы transport-level за пределами обычного HTTP path.

**Изменения:**

- Явное блокирование direct TCP на произвольные порты в strict.
- Явное блокирование UDP non-53 в strict.
- Документированное детерминированное поведение для IPv6 (blocked или unsupported с явной ошибкой).
- Документированное поведение для DoH/DoT (нет hidden bypass; зависит от allowlist policy endpoint).

**Acceptance:**

- FR-2 усилен до adversarial transport cases.
- Поведение IPv6/DoH/DoT формализовано в коде и доках.

**Тесты slice 4 (полные спецификации):**

#### D1. Direct TCP other ports (negative)
- **Test:** raw TCP to non-HTTP ports
- **Expected:** blocked
- **Where:** Go e2e

#### D2. UDP non-53 (negative)
- **Test:** send UDP to arbitrary port
- **Expected:** blocked in strict
- **Where:** Go e2e

#### D3. IPv6 attempt (negative/explicit)
- **Test:** IPv6 outbound attempt
- **Expected:** explicitly blocked or explicitly unsupported with deterministic error
- **Where:** Go e2e/daemon unit (documented behavior required)

#### D4. DoH/DoT behavior (explicit)
- **Test:** attempt DNS over HTTPS/TLS channel
- **Expected:** documented behavior (allowed only if host allowlist allows endpoint; no hidden bypass)
- **Where:** Go e2e note + doc assertion

**Проверка после slice:**

- Targeted: adversarial transport suites
- `nix develop --command go test ./... -count=1`
- `nix develop --command bash -lc 'cd sdk && npm test'`

---

### Slice 5 — Robustness and isolation at scale (concurrency, residue, cycles)

**Связь с PR:** PR-C (вторая часть)

**Цель:** доказать надёжность lifecycle при конкуренции и отказах.

**Изменения:**

- Hardening cleanup paths при crash/partial-start.
- Startup cleanup routine для stale anchors/rules (best effort, deterministic behavior).
- Защита от cross-VM interference в concurrent strict VM.

**Acceptance:**

- FR-4/NFR-1..4 подтверждены стресс- и интеграционными сценариями.

**Тесты slice 5 (полные спецификации):**

#### E3. Daemon crash/restart residue check
- **Test:** simulate abrupt termination path
- **Expected:** on next start no stale conflicting rules (or cleanup routine handles leftovers)
- **Where:** integration test (best effort acceptable, but must exist)

#### E4. Repeated start/stop cycles
- **Test:** N cycles for one VM
- **Expected:** no rule leak/no growth
- **Where:** Go e2e/integration

#### F1. Multi-VM concurrent strict
- **Test:** 2–3 VMs with different policies
- **Expected:** each VM obeys own policy; no cross-VM interference
- **Where:** Go e2e

#### F2. Stop one VM, other continues
- **Test:** terminate VM-A while VM-B active
- **Expected:** VM-B policy unchanged and working
- **Where:** Go e2e

**Проверка после slice:**

- Targeted: lifecycle/concurrency suites
- `nix develop --command go test ./... -count=1`
- `nix develop --command bash -lc 'cd sdk && npm test'`

---

### Slice 6 — Secrets/MITM regression + observability counters completion

**Связь с PR:** PR-C (третья часть)

**Цель:** убедиться, что strict не ломает security-sensitive flows и добавить финальную observability.

**Изменения:**

- Проверка secret injection в strict при allowlisted хостах.
- Проверка отсутствия инъекции при host mismatch.
- Финализация counters (`blocked_egress`, `dns_blocked`) и shutdown/reporting логов.

**Acceptance:**

- FR-5 выполнен полностью.
- FR-6 подтверждён (без регрессий DX).

**Тесты slice 6 (полные спецификации):**

#### G1. Secret injection strict (positive)
- **Test:** allowed host, placeholder inside guest, real secret on wire
- **Expected:** pass (no secret leak in guest output)
- **Where:** Go e2e + SDK

#### G2. Secret host mismatch (negative)
- **Test:** secret configured for host A, request to host B
- **Expected:** no injection
- **Where:** Go e2e or SDK

#### H2. Shutdown counters
- **Expected logs/counters:** non-zero `blocked_egress` / `dns_blocked` in denial scenarios
- **Where:** integration assertions

**Проверка после slice:**

- Targeted: MITM + observability suites
- `nix develop --command go test ./... -count=1`
- `nix develop --command bash -lc 'cd sdk && npm test'`

---

### Slice 7 — Документация + full matrix gate + final sign-off

**Цель:** завершить выпуск с полной трассируемостью требований.

**Изменения:**

- Обновить документацию:
  - `docs/network-enforcement.md`
  - `README.md` (кратко про strict + platform guarantees/limitations)
  - `spec.md` (статус/чекбоксы по выполненному слайсу).
- Явно зафиксировать отсутствие silent fallback strict -> compat.
- Свести FR/NFR -> tests traceability matrix (ID -> test -> status).

**Acceptance:**

- Все FR-1..FR-6 закрыты.
- Матрица A–H полностью покрыта и зелёная.
- Доки обновлены.

**Проверка после slice (обязательная):**

- `nix develop --command go test ./...`
- `nix develop --command bash -lc 'cd sdk && npm test'`

**Дополнительно (рекомендуется):**

- Targeted strict/dns suites отдельно с `-count=1`.

---

## 7) Полная матрица тестов A–H (распределение по slices)

- **Slice 1:** A1, E1, E2, H1
- **Slice 2:** A2, C1, C2, C3
- **Slice 3:** B1, B2, B3, B4, B5
- **Slice 4:** D1, D2, D3, D4
- **Slice 5:** E3, E4, F1, F2
- **Slice 6:** G1, G2, H2
- **Slice 7:** full regression gate + docs + traceability confirmation

---

## 8) Обязательные команды валидации

- `nix develop --command go test ./...`
- `nix develop --command bash -lc 'cd sdk && npm test'`

Дополнительно (рекомендуется):

- targeted strict/dns suites отдельно с `-count=1`.

---

## 9) Definition of Done

Задача считается завершённой, если:

1. FR-1..FR-6 реализованы.
2. Матрица тестов A–H покрыта (все обязательные кейсы зелёные).
3. `go test ./...` и `sdk npm test` зелёные в Nix.
4. Документация обновлена:
   - `docs/network-enforcement.md`
   - `README.md` (кратко про strict + platform guarantees/limitations)
   - `spec.md` (статус/чекбоксы по выполненному слайсу).
5. Нет silent fallback из strict в compat.

---

## 10) Примечание по выполнению в Ralph loop

Каждый slice в Ralph loop фиксировать как отдельную итерацию/подитерацию с обязательным артефактом:

- список изменённых файлов,
- список тестов slice (ID + результат),
- короткий rollback/risks note,
- решение: `proceed / rework`.

Это гарантирует, что проверка происходит **после каждого slice**, а не только в финале.
