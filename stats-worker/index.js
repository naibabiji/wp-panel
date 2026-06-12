// WP Panel 匿名安装统计 Worker
// POST /api/heartbeat — 面板匿名心跳上报
// GET  /api/stats     — 公开统计（total + active_24h 滚动窗口）

export default {
  async fetch(request, env) {
    const url = new URL(request.url);
    const corsHeaders = {
      'Access-Control-Allow-Origin': '*',
      'Access-Control-Allow-Methods': 'GET, POST, OPTIONS',
      'Access-Control-Allow-Headers': 'Content-Type',
    };

    if (request.method === 'OPTIONS') {
      return new Response(null, { headers: corsHeaders });
    }

    // 公开统计 — 直接读计数器，零次 list() 调用
    if (request.method === 'GET' && url.pathname === '/api/stats') {
      const stats = await getStats(env);
      return new Response(JSON.stringify(stats), {
        headers: {
          ...corsHeaders,
          'Content-Type': 'application/json',
          'Cache-Control': 'public, max-age=3600',
        },
      });
    }

    // 匿名心跳 — 面板定时上报
    if (request.method === 'POST' && url.pathname === '/api/heartbeat') {
      try {
        const body = await request.json();
        const { anonymous_id, version } = body;
        if (!anonymous_id || typeof anonymous_id !== 'string' || anonymous_id.length < 8) {
          return new Response(JSON.stringify({ error: 'invalid anonymous_id' }), {
            status: 400,
            headers: { ...corsHeaders, 'Content-Type': 'application/json' },
          });
        }
        await saveHeartbeat(env, anonymous_id, version || 'unknown');
        return new Response(JSON.stringify({ ok: true }), {
          headers: { ...corsHeaders, 'Content-Type': 'application/json' },
        });
      } catch {
        return new Response(JSON.stringify({ error: 'invalid request' }), {
          status: 400,
          headers: { ...corsHeaders, 'Content-Type': 'application/json' },
        });
      }
    }

    // 一次性初始化计数器（迁移后用 curl 调用一次即可删掉这个分支）
    if (request.method === 'POST' && url.pathname === '/api/migrate') {
      await migrateCounters(env);
      return new Response(JSON.stringify({ migrated: true }), {
        headers: { ...corsHeaders, 'Content-Type': 'application/json' },
      });
    }

    return new Response('Not Found', { status: 404 });
  },
};

// 从已有的 id:* 键重建 total 计数器和 24h 小时槽
async function migrateCounters(env) {
  let total = 0;
  const now = Date.now();
  const hourCounters = {}; // hourKey → count

  let cursor;
  do {
    const result = await env.STATS_KV.list({ prefix: 'id:', cursor, limit: 1000 });
    total += result.keys.length;

    for (const key of result.keys) {
      try {
        const data = await env.STATS_KV.get(key.name, { type: 'json' });
        if (!data || !data.last) continue;

        const lastTime = new Date(data.last).getTime();
        const hoursAgo = (now - lastTime) / 3600000;
        if (hoursAgo < 0 || hoursAgo > 24) continue;

        const hourKey = new Date(lastTime).toISOString().slice(0, 13).replace('T', '-');
        const anonymousId = key.name.slice(3); // remove "id:" prefix
        const hourlyMarker = `hourly:${hourKey}:${anonymousId}`;

        const existingMarker = await env.STATS_KV.get(hourlyMarker);
        if (!existingMarker) {
          await env.STATS_KV.put(hourlyMarker, '1', { expirationTtl: 172800 });
          hourCounters[hourKey] = (hourCounters[hourKey] || 0) + 1;
        }
      } catch {
        // skip corrupted records
      }
    }

    cursor = result.list_complete ? undefined : result.cursor;
  } while (cursor);

  // 写入 total
  await env.STATS_KV.put('meta:total', String(total));

  // 写入所有小时计数器
  const writes = [];
  for (const [hourKey, count] of Object.entries(hourCounters)) {
    const hourlyCounter = `active:hour:${hourKey}`;
    const cur = parseInt(await env.STATS_KV.get(hourlyCounter)) || 0;
    writes.push(env.STATS_KV.put(hourlyCounter, String(cur + count), { expirationTtl: 172800 }));
  }
  await Promise.all(writes);
}

// 汇总过去 24 个整点小时槽的独立服务器数
async function getStats(env) {
  const now = new Date();
  const hourKeys = [];
  for (let i = 0; i < 24; i++) {
    const d = new Date(now.getTime() - i * 3600000);
    hourKeys.push(`active:hour:${d.toISOString().slice(0, 13).replace('T', '-')}`);
  }

  const [total, ...hourlyCounts] = await Promise.all([
    env.STATS_KV.get('meta:total'),
    ...hourKeys.map(k => env.STATS_KV.get(k)),
  ]);

  const active = hourlyCounts.reduce((sum, v) => sum + (parseInt(v) || 0), 0);

  return {
    total: parseInt(total) || 0,
    active_24h: active,
    active: active,
  };
}

// 写入心跳时同步更新计数器（按小时粒度，支持 24h 滚动窗口）
async function saveHeartbeat(env, anonymousId, version) {
  const now = new Date().toISOString();
  const hourKey = now.slice(0, 13).replace('T', '-');
  const idKey = `id:${anonymousId}`;
  const hourlyMarker = `hourly:${hourKey}:${anonymousId}`;

  const [existing, hourlyExists] = await Promise.all([
    env.STATS_KV.get(idKey, { type: 'json' }),
    env.STATS_KV.get(hourlyMarker),
  ]);

  const writes = [];

  writes.push(env.STATS_KV.put(idKey, JSON.stringify({
    v: version,
    first: existing?.first || now,
    last: now,
  })));

  // 新安装 → 总数 +1
  if (!existing) {
    const total = parseInt(await env.STATS_KV.get('meta:total')) || 0;
    writes.push(env.STATS_KV.put('meta:total', String(total + 1)));
  }

  // 本小时首次心跳 → 当前小时活跃数 +1
  if (!hourlyExists) {
    writes.push(env.STATS_KV.put(hourlyMarker, '1', { expirationTtl: 172800 }));
    const hourlyCounter = `active:hour:${hourKey}`;
    const cur = parseInt(await env.STATS_KV.get(hourlyCounter)) || 0;
    writes.push(env.STATS_KV.put(hourlyCounter, String(cur + 1), { expirationTtl: 172800 }));
  }

  await Promise.all(writes);
}
