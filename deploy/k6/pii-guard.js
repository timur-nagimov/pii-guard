// Сценарий k6 для сервиса маскирования персональных данных.
//
// Это второе независимое мнение рядом с собственными генераторами cmd/loadgen
// и cmd/sonar: k6 считает задержку и ошибки своим кодом, поэтому совпадение
// чисел означает, что измеритель не обманывает.
//
// Одна итерация состоит из пары запросов по контракту сервиса:
//   1) прямой шаг: текст с персональными данными, ответ обязан отличаться от входа;
//   2) обратный шаг: та же маска с тем же payload_id, ответ обязан совпасть
//      с исходным текстом побайтово.
//
// Запуск на машине генератора:
//   BASE_URL=http://10.129.0.22 RPS=1000 DURATION=5m k6 run pii-guard.js
//
// Отдача показателей в Prometheus через remote write:
//   K6_PROMETHEUS_RW_SERVER_URL=http://10.129.0.22:9090/api/v1/write \
//   K6_PROMETHEUS_RW_TREND_STATS=p(95),p(99),max \
//   k6 run -o experimental-prometheus-rw pii-guard.js
// На стороне Prometheus нужен ключ запуска --web.enable-remote-write-receiver.
import http from 'k6/http';
import { check, sleep } from 'k6';
import { Counter, Rate, Trend } from 'k6/metrics';
import exec from 'k6/execution';

// trimTrailingSlashes срезает хвостовые косые черты проходом с конца.
// Выражение /\/+$/ делает то же самое, но откатывается: на адресе из сотен
// косых черт подряд движок отдаёт по одному символу и всякий раз заново
// проверяет конец строки, и разбор растёт квадратом от длины адреса.
function trimTrailingSlashes(url) {
  let end = url.length;
  while (end > 0 && url[end - 1] === '/') end -= 1;
  return url.slice(0, end);
}

const BASE_URL = trimTrailingSlashes(__ENV.BASE_URL || 'http://127.0.0.1:8080');
const RPS = Number(__ENV.RPS || 200);
const DURATION = __ENV.DURATION || '1m';
const PAYLOAD_SIZE = Number(__ENV.PAYLOAD_SIZE || 250);
const SYSTEM_KEY = __ENV.SYSTEM_KEY || '';
const P99_MS = Number(__ENV.P99_MS || 50);
const ERROR_RATE = Number(__ENV.ERROR_RATE || 0.01);
const RETRIES = Number(__ENV.RETRIES || 2);
// Отправителей берём с запасом: при частоте в тысячу пар и задержке в
// миллисекунду хватает десятков, но запас спасает от всплесков задержки.
const VUS = Number(__ENV.VUS || Math.max(50, Math.ceil(RPS / 5)));

// Собственные показатели: доля верных масок, доля точного восстановления
// и число ответов с просьбой повторить.
const maskChanged = new Rate('pii_mask_changed');
const demaskExact = new Rate('pii_demask_exact');
const throttled = new Counter('pii_throttled');
// Тело, которое не разобралось как JSON, раньше просто пропадало. Под
// нагрузкой это важный сигнал: сервис ответил двухсотым кодом, но отдал не то,
// что обещает контракт. Считаем такие ответы отдельно.
const badBody = new Counter('pii_bad_body');
const pairDuration = new Trend('pii_pair_duration', true);

export const options = {
  scenarios: {
    pairs: {
      executor: 'constant-arrival-rate',
      rate: RPS,
      timeUnit: '1s',
      duration: DURATION,
      preAllocatedVUs: VUS,
      maxVUs: VUS * 4,
    },
  },
  thresholds: {
    'http_req_failed': [`rate<${ERROR_RATE}`],
    'http_req_duration{step:mask}': [`p(99)<${P99_MS}`],
    'http_req_duration{step:demask}': [`p(99)<${P99_MS}`],
    'pii_mask_changed': ['rate>0.99'],
    'pii_demask_exact': ['rate>0.99'],
  },
  summaryTrendStats: ['avg', 'min', 'med', 'p(95)', 'p(99)', 'max'],
  noConnectionReuse: false,
};

// byteLength считает длину строки в байтах кодировки UTF-8: размер текста
// в задании задан в байтах, а кириллическая буква занимает два.
function byteLength(text) {
  let n = 0;
  for (const ch of text) {
    const code = ch.codePointAt(0);
    if (code < 0x80) n += 1;
    else if (code < 0x800) n += 2;
    else if (code < 0x10000) n += 3;
    else n += 4;
  }
  return n;
}

// makeText собирает текст нужного размера с персональными данными внутри.
// Номер итерации попадает в текст, чтобы запросы не были копиями друг друга.
function makeText(seq, size) {
  const base =
    `Клиент Иванов Иван Иванович, паспорт 4509 12${String(3400 + (seq % 100)).slice(0, 4)}, ` +
    `тел. +7 916 123-45-67, ИНН 500100732259, карта 4111 1111 1111 1111. `;
  let text = base;
  while (byteLength(text) < size) text += base;
  return text;
}

// postProcess шлёт один запрос контракта и повторяет его при ответе
// с просьбой повторить: по правилам проверяющей системы код 429 означает
// не ошибку, а просьбу прийти позже.
function postProcess(payload, payloadID, step) {
  const headers = { 'Content-Type': 'application/json' };
  if (SYSTEM_KEY) headers['X-System-Key'] = SYSTEM_KEY;
  const params = { headers, tags: { step }, timeout: '10s' };
  const body = JSON.stringify({ payload: payload, payload_id: payloadID });

  for (let attempt = 0; attempt <= RETRIES; attempt++) {
    const res = http.post(`${BASE_URL}/process`, body, params);
    if (res.status !== 429) return res;
    throttled.add(1, { step });
    sleep(0.2 * (attempt + 1));
  }
  return null;
}

// resultOf достаёт поле result из ответа, не роняя прогон на чужом теле.
function resultOf(res) {
  if (res?.status !== 200) return null;
  try {
    const parsed = res.json();
    return typeof parsed.result === 'string' ? parsed.result : null;
  } catch (err) {
    // Причину не печатаем намеренно: на тысяче пар в секунду вывод разбора сам
    // станет узким местом и исказит то, что мы измеряем. Но и терять событие
    // нельзя — считаем его, а имя ошибки кладём меткой, чтобы в показателях
    // было видно, чем именно тело не понравилось.
    badBody.add(1, { reason: err && err.name ? err.name : 'unknown' });
    return null;
  }
}

// pairIteration это одна итерация нагрузки: прямой шаг и обратный к нему.
export default function pairIteration() {
  const seq = exec.scenario.iterationInTest;
  const payloadID = `k6-${exec.vu.idInTest}-${seq}`;
  const original = makeText(seq, PAYLOAD_SIZE);
  const started = Date.now();

  const maskRes = postProcess(original, payloadID, 'mask');
  const masked = resultOf(maskRes);
  const maskOK = check(maskRes, {
    'прямой шаг: код 200': (r) => r !== null && r.status === 200,
    'прямой шаг: текст изменён': () => masked !== null && masked !== original,
  });
  maskChanged.add(maskOK);
  if (!maskOK || masked === null) return;

  const backRes = postProcess(masked, payloadID, 'demask');
  const restored = resultOf(backRes);
  const backOK = check(backRes, {
    'обратный шаг: код 200': (r) => r !== null && r.status === 200,
    'обратный шаг: совпало с исходным': () => restored === original,
  });
  demaskExact.add(backOK);
  pairDuration.add(Date.now() - started);
}
