const DAY = 86400000;

const categories = new Map([
  ['lesson', { label: 'Пара', priority: 0 }],
  ['event', { label: 'Мероприятие', priority: 1 }],
  ['quiz', { label: 'Проверочная', priority: 1 }],
  ['test', { label: 'Контрольная', priority: 2 }],
  ['exam', { label: 'Экзамен', priority: 2 }],
  ['deadline', { label: 'Дедлайн', priority: 2 }],
  ['other', { label: 'Прочее', priority: 0 }],
]);

export function eventCategory(event) {
  const key = categories.has(event.category) ? event.category : categories.has(event.kind) ? event.kind : 'other';
  return { key, ...categories.get(key) };
}

export function localDay(value, timezone = 'Europe/Moscow') {
  const parts = new Intl.DateTimeFormat('en-US', {
    timeZone: timezone, year: 'numeric', month: '2-digit', day: '2-digit',
  }).formatToParts(new Date(value));
  const get = type => parts.find(part => part.type === type).value;
  return `${get('year')}-${get('month')}-${get('day')}`;
}

const calendarDate = day => new Date(`${day}T00:00:00Z`);
const dayKey = date => date.toISOString().slice(0, 10);

// Find the first instant of a local day, including midnight DST transitions.
export function dayStart(day, timezone) {
  const middle = calendarDate(day).getTime();
  let low = middle - 36 * 3600000;
  let high = middle + 36 * 3600000;
  while (high - low > 1) {
    const probe = Math.floor((low + high) / 2);
    if (localDay(probe, timezone) < day) low = probe;
    else high = probe;
  }
  return new Date(high);
}

export function periodRange(anchor, view, timezone = 'Europe/Moscow') {
  const start = calendarDate(anchor);
  if (view === 'month') start.setUTCDate(1);
  else start.setUTCDate(start.getUTCDate() - (start.getUTCDay() + 6) % 7);
  const end = new Date(start);
  if (view === 'month') end.setUTCMonth(end.getUTCMonth() + 1);
  else end.setUTCDate(end.getUTCDate() + 7);
  const days = [];
  for (let date = +start; date < +end; date += DAY) days.push(dayKey(new Date(date)));
  return { from: dayStart(dayKey(start), timezone), to: dayStart(dayKey(end), timezone), days };
}

export function shiftPeriod(anchor, view, direction) {
  const date = calendarDate(anchor);
  if (view === 'month') {
    date.setUTCDate(1);
    date.setUTCMonth(date.getUTCMonth() + direction);
  } else date.setUTCDate(date.getUTCDate() + direction * 7);
  return dayKey(date);
}

export function gridDays(days, view) {
  if (view !== 'month') return days;
  const start = calendarDate(days[0]);
  start.setUTCDate(start.getUTCDate() - (start.getUTCDay() + 6) % 7);
  return Array.from({ length: 42 }, (_, i) => dayKey(new Date(+start + i * DAY)));
}

export function selectedDate(days, selected, today) {
  return days.includes(selected) ? selected : days.includes(today) ? today : days[0];
}

export function viewportFloor(scrollY, height) {
  return Math.ceil(scrollY + height);
}

export function isCurrentLesson(event, current) {
  return Boolean(current && (event.kind || eventCategory(event).key) === 'lesson' &&
    Number.isSafeInteger(current.id) && event.id === current.id &&
    Date.parse(event.starts_at) === Date.parse(current.starts_at));
}

export function groupEvents(events, range, timezone = 'Europe/Moscow') {
  if (events !== null && !Array.isArray(events)) throw new Error('Invalid events');
  const groups = new Map(range.days.map(day => [day, []]));
  for (const event of events || []) {
    if (!event || typeof event.title !== 'string' || typeof event.starts_at !== 'string' ||
        !Number.isFinite(Date.parse(event.starts_at)) ||
        (event.ends_at != null && !Number.isFinite(Date.parse(event.ends_at))) ||
        (event.location != null && typeof event.location !== 'string')) throw new Error('Invalid event');
    const instant = new Date(event.starts_at);
    if (instant >= range.from && instant < range.to) groups.get(localDay(instant, timezone))?.push(event);
  }
  for (const events of groups.values()) events.sort((a, b) => Date.parse(a.starts_at) - Date.parse(b.starts_at));
  return groups;
}

if (typeof document !== 'undefined') {
  const root = document.querySelector('main');
  const timezone = root.dataset.timezone || 'Europe/Moscow';
  const $ = selector => document.querySelector(selector);
  const format = (value, options) => new Intl.DateTimeFormat('ru-RU', { timeZone: timezone, ...options }).format(new Date(value));
  const node = (tag, text, className) => {
    const element = document.createElement(tag);
    if (text !== undefined) element.textContent = text;
    if (className) element.className = className;
    return element;
  };
  let view = 'week';
  let anchor = null; // A null anchor follows the current period across midnight.
  let controller;
  let request = 0;
  let displayedPeriod = '';
  let selected = localDay(Date.now(), timezone);
  let groups = new Map();
  let range;
  let loaded = false;
  let current = null;
  let statusAt = 0;

  function stable(update) {
    const x = window.scrollX, y = window.scrollY;
    // ponytail: retain only the viewport floor until reload; no async scroll restoration.
    root.style.minHeight = `${viewportFloor(y, window.innerHeight)}px`;
    update();
    if (window.scrollY !== y) window.scrollTo({ left: x, top: y, behavior: 'instant' });
  }

  function render(today = localDay(Date.now(), timezone)) {
    const focused = document.activeElement?.dataset.day;
    const fragment = document.createDocumentFragment();
    for (const day of gridDays(range.days, view)) {
      const events = groups.get(day) || [];
      const inPeriod = range.days.includes(day);
      const button = node('button', undefined, `date-cell${inPeriod ? '' : ' outside'}`);
      button.dataset.day = day;
      button.disabled = !inPeriod;
      button.setAttribute('aria-pressed', String(day === selected));
      if (day === today) button.setAttribute('aria-current', 'date');
      button.setAttribute('aria-label', `${format(dayStart(day, timezone), { weekday: 'long', day: 'numeric', month: 'long', year: 'numeric' })}${day === today ? ', сегодня' : ''}, ${loaded && inPeriod ? `событий: ${events.length}` : 'данные не загружены'}${events.length ? `, ${[...new Set(events.map(event => eventCategory(event).label))].join(', ')}` : ''}`);
      button.append(node('span', String(Number(day.slice(-2))), 'date-number'));
      const markers = node('span', undefined, 'date-markers');
      markers.setAttribute('aria-hidden', 'true');
      for (const key of [...new Set(events.map(event => eventCategory(event).key))].slice(0, 3)) markers.append(node('i', undefined, `dot category-${key}`));
      button.append(markers);
      button.addEventListener('click', () => stable(() => { selected = day; render(); }));
      button.addEventListener('keydown', event => {
        const index = range.days.indexOf(day);
        const delta = { ArrowLeft: -1, ArrowRight: 1, ArrowUp: -7, ArrowDown: 7, Home: -index, End: range.days.length - 1 - index }[event.key];
        if (delta === undefined) return;
        event.preventDefault();
        const target = range.days[Math.max(0, Math.min(range.days.length - 1, index + delta))];
        $(`#dates [data-day="${target}"]`).focus({ preventScroll: true });
      });
      fragment.append(button);
    }
    $('#dates').replaceChildren(fragment);
    if (focused) $(`#dates [data-day="${focused}"]:not(:disabled)`)?.focus({ preventScroll: true });
      const events = groups.get(selected) || [];
      const section = $('#agenda');
      const heading = node('h4');
      heading.id = 'agenda-title';
      const date = node('time', format(dayStart(selected, timezone), { weekday: 'long', day: 'numeric', month: 'long' }));
      date.dateTime = selected;
      heading.append(date);
       section.replaceChildren(heading);
      for (const event of events) {
        const category = eventCategory(event);
        const article = node('article', undefined, `importance-${category.priority}`);
        const time = node('time', event.all_day ? 'Весь день' : format(event.starts_at, { hour: '2-digit', minute: '2-digit' }) +
          (event.ends_at ? ` – ${format(event.ends_at, { hour: '2-digit', minute: '2-digit' })}` : ''));
        time.dateTime = event.starts_at;
        const details = node('div', undefined, 'event-details');
        const badge = node('span', category.label, `category category-${category.key}`);
        details.append(node('strong', event.title), badge);
        if (event.ends_at && !event.all_day) details.append(node('span', `${Math.round((Date.parse(event.ends_at) - Date.parse(event.starts_at)) / 60000)} мин`, 'duration'));
        if (isCurrentLesson(event, current)) details.append(node('span', 'Сейчас идёт', 'active-lesson'));
        article.append(time, details);
        if (event.location) article.append(node('span', event.location));
        section.append(article);
      }
      if (!events.length) section.append(node('p', loaded ? 'Нет событий' : 'Загружаем расписание…', 'empty-day'));
  }

  async function refresh() {
    controller?.abort();
    controller = new AbortController();
    const active = controller;
    const id = ++request;
    const timeout = setTimeout(() => active.abort(), 15000);
    const today = localDay(Date.now(), timezone);
    const nextRange = periodRange(anchor || today, view, timezone);
    const key = `${view}:${nextRange.days[0]}`;
    stable(() => {
    range = nextRange;
    selected = selectedDate(range.days, selected, today);
    $('#schedule').dataset.view = view;
    $('#period-title').textContent = view === 'month'
      ? format(range.from, { month: 'long', year: 'numeric' }).replace(/\s*г\.$/, '')
      : new Intl.DateTimeFormat('ru-RU', { timeZone: timezone, day: 'numeric', month: 'short',
        ...(range.days.some(day => day.slice(0, 4) !== today.slice(0, 4)) ? { year: 'numeric' } : {})
      }).formatRange(range.from, dayStart(range.days.at(-1), timezone));
    if (key !== displayedPeriod) { groups = new Map(); loaded = false; }
    render(today);
    $('#schedule').setAttribute('aria-busy', 'true');
    $('#feedback').textContent = 'Обновляю расписание...';
    $('#error').replaceChildren();
    });
    const get = async url => {
      const response = await fetch(url, { signal: active.signal, cache: 'no-store' });
      if (!response.ok) throw new Error('Request failed');
      return response.json();
    };
    try {
      const results = await Promise.allSettled([
        get(`/api/public/v1/schedule?${new URLSearchParams({ from: nextRange.from.toISOString(), to: nextRange.to.toISOString() })}`)
          .then(data => groupEvents(data.events, nextRange, timezone)),
        get('/api/public/v1/status').then(data => {
          if (!data || typeof data !== 'object') throw new Error('Invalid status');
           const event = data.current;
           if (event) groupEvents([event], nextRange, timezone);
          return data;
        }),
      ]);
      if (id !== request) return;
      stable(() => {
       const [scheduleResult, statusResult] = results;
       current = statusResult.status === 'fulfilled' ? statusResult.value.current || null : null;
       statusAt = Date.now();
      if (scheduleResult.status === 'fulfilled') {
        groups = scheduleResult.value;
        loaded = true;
        displayedPeriod = key;
        const count = [...scheduleResult.value.values()].reduce((sum, events) => sum + events.length, 0);
        $('#feedback').textContent = count ? `Событий за период: ${count}` : 'В этом периоде событий нет.';
      } else $('#feedback').textContent = displayedPeriod === key ? 'Показаны ранее загруженные данные.' : 'Расписание не загружено.';
      render();
      if (results.some(result => result.status === 'rejected')) {
        const retry = node('button', 'Повторить');
        retry.addEventListener('click', refresh);
        $('#error').append(node('span', 'Не удалось обновить данные. Проверьте соединение. '), retry);
      }
      });
    } finally {
      clearTimeout(timeout);
      if (id === request) $('#schedule').setAttribute('aria-busy', 'false');
    }
  }

  $('#view').addEventListener('click', event => {
    const button = event.target.closest('button[data-view]');
    if (!button || !$('#view').contains(button) || !['week', 'month'].includes(button.dataset.view) || button.dataset.view === view) return;
    view = button.dataset.view;
    for (const control of $('#view').querySelectorAll('button')) control.setAttribute('aria-pressed', String(control === button));
    refresh();
  });
  for (const [selector, direction] of [['#previous', -1], ['#next', 1]]) $(selector).addEventListener('click', () => {
    anchor = shiftPeriod(anchor || localDay(Date.now(), timezone), view, direction);
    refresh();
  });
  let clockDay;
  const tick = () => {
    const now = Date.now();
    if (current && (now - statusAt >= 60000 || (current.ends_at && now >= Date.parse(current.ends_at)))) {
      current = null;
      stable(() => document.querySelectorAll('.active-lesson').forEach(marker => marker.remove()));
    }
    $('#clock').textContent = format(now, { hour: '2-digit', minute: '2-digit', second: '2-digit' });
    const today = localDay(now, timezone);
    $('#today-date').textContent = format(now, { day: 'numeric', month: 'long', year: 'numeric' });
    $('#today-date').dateTime = today;
    if (clockDay && today !== clockDay) refresh();
    clockDay = today;
  };
  tick();
  setInterval(tick, 1000);
  setInterval(() => { if (!document.hidden) refresh(); }, 60000);
  document.addEventListener('visibilitychange', () => { if (!document.hidden) refresh(); });
  refresh();
}
