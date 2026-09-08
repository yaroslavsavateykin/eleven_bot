// Run: node internal/web/static/app.test.mjs
import assert from 'node:assert/strict';
import { localDay, dayStart, periodRange, shiftPeriod, groupEvents, eventCategory, gridDays, selectedDate, viewportFloor, isCurrentLesson } from './app.js';
import { readFileSync } from 'node:fs';

assert.equal(eventCategory({ kind: 'lesson' }).key, 'lesson');
assert.equal(eventCategory({ kind: 'lesson', category: 'exam' }).priority, 2);
assert.equal(eventCategory({ category: '__proto__' }).key, 'other');
for (const category of ['lesson', 'event', 'test', 'quiz', 'exam', 'deadline', 'other']) assert.ok(eventCategory({ category }).label);
assert.ok(!readFileSync(new URL('../templates/index.html', import.meta.url), 'utf8').includes('В этот момент'));

assert.equal(localDay('2026-09-06T21:30:00Z'), '2026-09-07');
assert.equal(localDay('2026-09-07T01:00:00Z', 'America/New_York'), '2026-09-06');
const week = periodRange('2026-09-07', 'week');
assert.equal(week.from.toISOString(), '2026-09-06T21:00:00.000Z');
assert.equal(week.to.toISOString(), '2026-09-13T21:00:00.000Z');
assert.equal(periodRange('2026-09-13', 'week').from.toISOString(), week.from.toISOString());
assert.equal(periodRange('2024-02-29', 'month').days.length, 29);
assert.equal(periodRange('2025-02-01', 'month').days.length, 28);
assert.equal(shiftPeriod('2026-01-31', 'month', 1), '2026-02-01');
assert.equal(shiftPeriod('2026-01-01', 'month', -1), '2025-12-01');
assert.equal(shiftPeriod('2025-12-29', 'week', 1), '2026-01-05');
for (const [day, hours] of [['2026-03-08', 167], ['2026-11-01', 169]]) {
  const range = periodRange(day, 'week', 'America/New_York');
  assert.equal((range.to - range.from) / 3600000, hours);
}
assert.equal(dayStart('2026-09-07', 'Asia/Kathmandu').toISOString(), '2026-09-06T18:15:00.000Z');
assert.equal(dayStart('2018-11-04', 'America/Sao_Paulo').toISOString(), '2018-11-04T03:00:00.000Z');
const event = (starts_at, title = '<img src=x onerror=alert(1)>') => ({ starts_at, title });
const grouped = groupEvents([
  event('2026-09-07T05:00:00Z', 'Later'), event('2026-09-06T21:00:00Z'),
  event('2026-09-06T20:59:59Z'), event('2026-09-13T21:00:00Z'),
], week);
assert.equal(grouped.size, 7);
assert.equal(grouped.get('2026-09-07').length, 2);
assert.equal(grouped.get('2026-09-07')[0].title, '<img src=x onerror=alert(1)>');
assert.equal([...grouped.values()].flat().length, 2);
assert.equal([...groupEvents(null, week).values()].flat().length, 0);
assert.throws(() => groupEvents({}, week));
assert.throws(() => groupEvents([event('invalid')], week));
assert.throws(() => groupEvents([event('2026-09-07', {})], week));
console.log('Calendar checks passed: timezone boundaries, DST, leap years, navigation, grouping, validation.');

for (const [anchor, first, last] of [
  ['2026-01-15', '2025-12-29', '2026-02-08'],
  ['2026-12-31', '2026-11-30', '2027-01-10'],
  ['2024-02-29', '2024-01-29', '2024-03-10'],
]) {
  const days = periodRange(anchor, 'month').days;
  const grid = gridDays(days, 'month');
  assert.equal(grid.length, 42);
  assert.equal(grid[0], first);
  assert.equal(grid.at(-1), last);
  assert.ok(days.every(day => grid.includes(day)));
  assert.equal(new Set(grid).size, 42);
}
assert.deepEqual(gridDays(week.days, 'week'), week.days);
assert.equal(selectedDate(week.days, '2026-09-09', '2026-09-07'), '2026-09-09');
assert.equal(selectedDate(week.days, '2026-09-09', '2026-09-08'), '2026-09-09');
assert.equal(selectedDate(week.days, '2026-08-09', '2026-09-08'), '2026-09-08');
assert.equal(selectedDate(week.days, '2026-08-09', '2026-08-08'), week.days[0]);
assert.equal(viewportFloor(900.5, 759), 1660);
assert.equal(viewportFloor(120, 759), 879); // Use the latest scroll, not the request-start scroll.
const source = readFileSync(new URL('./app.js', import.meta.url), 'utf8');
const stylesheet = readFileSync(new URL('./style.css', import.meta.url), 'utf8');
assert.match(stylesheet, /\.dot\.category-event,\.dot\.category-quiz\{background:#9a78b5\}/);
assert.match(stylesheet, /\.dot\.category-test,\.dot\.category-exam,\.dot\.category-deadline\{background:#c47a2c\}/);
assert.match(stylesheet, /article \.category-event,article \.category-quiz\{color:#65427e;background:#eee4f7\}/);
assert.match(stylesheet, /article \.category-test,article \.category-exam,article \.category-deadline\{color:#7b430b;background:#f7e5c7;border:1px solid #d5a15c\}/);
assert.match(stylesheet, /\.dot\{[^}]*background:var\(--green-mid\)\}/);
assert.match(stylesheet, /article \.category\{[^}]*color:#365747;background:var\(--green-soft\)/);
assert.ok(!source.includes("$('#schedule').replaceChildren"));
assert.ok(source.includes('if (id !== request) return;'));
assert.ok(!source.includes('scrollIntoView'));
assert.ok(!source.includes("markers.append(node('small'"));
assert.ok(!source.includes(' · Важно'));
const template = readFileSync(new URL('../templates/index.html', import.meta.url), 'utf8');
assert.ok(!template.includes('id="status"'));
assert.ok(!source.includes('Сейчас пары нет'));
const current = { id: 7, kind: 'lesson', starts_at: '2026-09-08T10:00:00Z' };
assert.equal(isCurrentLesson(current, current), true);
assert.equal(isCurrentLesson({ ...current, category: 'exam' }, current), true);
assert.equal(isCurrentLesson({ ...current, starts_at: '2026-09-09T10:00:00Z' }, current), false);
assert.equal(isCurrentLesson({ ...current, id: 8 }, current), false);
assert.equal(isCurrentLesson({ ...current, kind: 'event' }, current), false);
assert.equal(isCurrentLesson(current, null), false);
assert.equal(isCurrentLesson({ ...current, id: undefined }, { ...current, id: undefined }), false);
assert.match(template, /id="view" aria-label="Вид расписания"/);
assert.ok(template.indexOf('<nav class="period-bar"') < template.indexOf('<h3 id="period-title">'));
assert.ok(!template.includes('Сегодня</button>'));
assert.ok(!template.includes('<select'));
assert.match(template, /data-view="week" aria-controls="schedule" aria-pressed="true"/);
assert.match(template, /data-view="month" aria-controls="schedule" aria-pressed="false"/);
assert.match(template, /id="previous" aria-label="Предыдущая" title="Предыдущая" aria-controls="schedule"><span aria-hidden="true">←<\/span><\/button>/);
assert.match(template, /id="next" aria-label="Следующая" title="Следующая" aria-controls="schedule"><span aria-hidden="true">→<\/span><\/button>/);
assert.ok(!template.match(/id="previous"[^>]*>Предыдущая<\/button>/));
assert.ok(!template.match(/id="next"[^>]*>Следующая<\/button>/));
assert.ok(source.includes("$('#view').addEventListener('click'"));
assert.ok(source.includes("control.setAttribute('aria-pressed', String(control === button))"));
assert.match(template, /id="feedback" class="sr-only"/);
assert.ok(!template.includes('class="glass'));
console.log('Grid, independent selection, and viewport stability checks passed.');
