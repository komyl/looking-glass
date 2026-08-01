'use strict';
function switchTab(name) {
    ['ping', 'traceroute', 'dig', 'port', 'ssl', 'bgp'].forEach(n => {
        document.getElementById('tab-' + n).classList.toggle('active', n === name);
        document.getElementById('panel-' + n).classList.toggle('active', n === name);
    });
    if (name === 'traceroute' || name === 'port') populateNodes();
}
function setStatus(id, state, text) { document.getElementById(id + '-dot').className = 'dot' + (state ? ' ' + state : ''); document.getElementById(id + '-stxt').textContent = text }
function esc(s) { return String(s).replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;') }
const ipInfoCache = {};
const traceLines = [];
let traceFlushTimer = null;
function enrichTraceLine(line, out) {
    traceLines.push(line);
    if (traceFlushTimer) clearTimeout(traceFlushTimer);
    traceFlushTimer = setTimeout(() => flushTraceLines(out), 150);
}
function flushTraceLines(out) {
    const lines = [...traceLines];
    traceLines.length = 0;
    const ips = [...new Set(lines.flatMap(l => [...l.matchAll(/\b(\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3})\b/g)].map(m => m[1])))];
    const unknown = ips.filter(ip => !(ip in ipInfoCache));
    const render = () => { lines.forEach(l => { out.innerHTML += renderTraceLine(l) + '\n' }); out.scrollTop = out.scrollHeight };
    if (!unknown.length) { render(); return }
    fetch('/api/ip-info?targets=' + unknown.map(encodeURIComponent).join(','))
        .then(r => r.json())
        .then(data => { Object.assign(ipInfoCache, data); render() })
        .catch(() => render());
}
function renderTraceLine(raw) {
    let s = esc(raw);

    if (/^(PING|traceroute to)/i.test(raw)) {
        return `<span class="t-head">${s}</span>`;
    }
    if (/^\s*\d+\s+\*\s+\*/.test(raw)) {
        return `<span class="t-star">${s}</span>`;
    }
    if (/unreachable|timed? ?out|failed|error|unknown host|bad address/i.test(raw)) {
        return `<span class="t-err">${s}</span>`;
    }
    if (/packets transmitted|round-trip|rtt\s/i.test(raw)) {
        return `<span class="t-ok">${s}</span>`;
    }

    s = s.replace(/(\d+(?:\.\d+)?)\s*(ms)/g, '<span class="t-time">$1 $2</span>');

    s = s.replace(/\b(\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3})\b/g, (match) => {
        const info = ipInfoCache[match];
        if (info && info.name) {
            return `<span class="t-ip">${match}</span><span style="color:var(--muted2);font-size:11px"> (${esc(info.asn)} ${esc(info.name)})</span>`;
        }
        return `<span class="t-ip">${match}</span>`;
    });

    return s;
}
function highlight(raw) {
    return renderTraceLine(raw);
}
function highlightDig(raw) {
    let s = esc(raw);
    if (raw.startsWith(';')) return `<span class="t-star">${s}</span>`;
    const m = raw.match(/^(\S+)\s+(\d+)\s+(\S+)\s+(\S+)\s+(.+)$/);
    if (m) {
        return `<span class="t-dns-key">${esc(m[1])}</span> <span class="t-dns-ttl">${esc(m[2])}</span> ${esc(m[3])} <span class="t-dns-type">${esc(m[4])}</span> <span class="t-dns-val">${esc(m[5])}</span>`;
    }
    return s;
}
let cachedNodeOpts = '';
function populateNodes() {
    if (cachedNodeOpts) {
        const tr = document.getElementById('tr-node');
        const port = document.getElementById('port-node');
        if (tr && !tr.options.length) tr.innerHTML = cachedNodeOpts;
        if (port && !port.options.length) port.innerHTML = cachedNodeOpts;
        return;
    }
    fetch('/api/nodes')
        .then(r => r.json())
        .then(nodes => {
            cachedNodeOpts = nodes.map(n => `<option value="${n.id}">${n.name}</option>`).join('');
            const tr = document.getElementById('tr-node');
            const port = document.getElementById('port-node');
            if (tr) tr.innerHTML = cachedNodeOpts;
            if (port) port.innerHTML = cachedNodeOpts;
        })
        .catch(() => { });
}
fetch('/api/myip').then(r => r.json()).then(d => { document.getElementById('clientIP').textContent = d.ip || 'unknown' }).catch(() => { document.getElementById('clientIP').textContent = 'unknown' });
populateNodes();
fetch('/api/info').then(r => r.json()).then(d => { const m = document.getElementById('hdrMeta'); if (d.route_count > 0) { m.innerHTML = `BGP Routes&nbsp;<span>${d.route_count.toLocaleString()}</span>&nbsp;&nbsp;Updated&nbsp;<span>${esc(d.bgp_updated)}</span>` } else { m.innerHTML = 'BGP data <span>not loaded</span>' } }).catch(() => { });

let pingES = null;
function runPingAll() {
    let target = document.getElementById('ping-target').value.trim();
    if (!target) return;
    target = target.replace(/^https?:\/\//i, '').replace(/\/.*$/, '');
    document.getElementById('ping-target').value = target;
    const out = document.getElementById('ping-out');
    setStatus('ping', 'run', 'Running...');
    document.getElementById('ping-run').disabled = true;
    out.innerHTML = `<table style="width:100%;border-collapse:collapse;font-size:13px">
<thead><tr style="color:var(--muted);font-size:11px;text-transform:uppercase;letter-spacing:.5px;border-bottom:1px solid var(--bdr)">
<th style="text-align:left;padding:8px 10px">Node</th>
<th style="text-align:center;padding:8px 10px">Sent/Recv</th>
<th style="text-align:center;padding:8px 10px">Loss</th>
<th style="text-align:right;padding:8px 10px">RTT min/avg/max</th>
<th style="text-align:center;padding:8px 10px">Status</th>
</tr></thead>
<tbody id="ping-tbody"></tbody>
</table>`;
    fetch('/api/nodes').then(r => r.json()).then(ns => {
        const tbody = document.getElementById('ping-tbody');
        ns.forEach(n => {
            tbody.innerHTML += `<tr id="ping-row-${n.id}" style="border-bottom:1px solid var(--bdr2)">
<td style="padding:10px 10px;font-weight:600;color:var(--text)">${esc(n.name)}</td>
<td style="padding:10px 10px;text-align:center;font-family:var(--mono)">—</td>
<td style="padding:10px 10px;text-align:center;font-family:var(--mono)">—</td>
<td style="padding:10px 10px;text-align:right;font-family:var(--mono)">—</td>
<td style="padding:10px 10px;text-align:center"><span class="spin"></span></td>
</tr>`;
        });
    });
    fetch('/api/ping-all?target=' + encodeURIComponent(target))
        .then(r => r.json())
        .then(data => {
            (data.results || []).forEach(r => {
                const row = document.getElementById('ping-row-' + r.id);
                if (!row) return;
                const cells = row.querySelectorAll('td');
                const statusColor = r.status === 'ok' ? 'var(--green)' : r.status === 'degraded' ? 'var(--yellow)' : 'var(--red)';
                const statusText = r.status === 'ok' ? 'OK' : r.status === 'degraded' ? 'Degraded' : r.status === 'down' ? 'Down' : 'Error';
                const lossColor = r.loss === 0 ? 'var(--green)' : r.loss < 50 ? 'var(--yellow)' : 'var(--red)';

                cells[1].textContent = r.error ? '—' : `${r.sent} / ${r.received}`;
                cells[2].innerHTML = r.error ? '—' : `<span style="color:${lossColor}">${esc(r.loss)}%</span>`;
                cells[3].textContent = r.error ? r.error : `${r.rtt_min} / ${r.rtt_avg} / ${r.rtt_max} ms`;
                cells[4].innerHTML = `<span style="color:${statusColor};font-weight:700;font-size:12px">${esc(statusText)}</span>`;
            });
            setStatus('ping', 'ok', 'Done');
            document.getElementById('ping-run').disabled = false;
        })
        .catch(err => {
            setStatus('ping', 'err', 'Error');
            document.getElementById('ping-out').innerHTML = `<span class="t-err">Request failed: ${esc(err.message)}</span>`;
            document.getElementById('ping-run').disabled = false;
        });
}

let trES = null;
function runTrace() {
    const target = document.getElementById('tr-target').value.trim();
    if (!target) return;
    const maxhops = document.getElementById('tr-hops').value;
    const out = document.getElementById('tr-out');
    stopTrace(); out.innerHTML = ''; setStatus('tr', 'run', 'Running...');
    document.getElementById('tr-run').disabled = true;
    document.getElementById('tr-stop').classList.remove('hidden');
    const trNode = document.getElementById('tr-node').value;
    const trURL = trNode
        ? '/api/proxy?node=' + trNode + '&action=traceroute&target=' + encodeURIComponent(target) + '&maxhops=' + maxhops
        : '/api/traceroute?target=' + encodeURIComponent(target) + '&maxhops=' + maxhops;
    trES = new EventSource(trURL);
    trES.onmessage = e => {
        if (e.data === '[DONE]') { stopTrace('ok'); return }
        if (e.data.startsWith('[ERROR]')) { out.innerHTML += `<span class="t-err">${esc(e.data.slice(7).trim())}</span>\n`; stopTrace('err'); return }
        if (e.data.trim()) enrichTraceLine(e.data, out);
    };
    trES.onerror = () => { if (trES && trES.readyState === 2) stopTrace('err') };
}
function stopTrace(state) {
    if (trES) { trES.close(); trES = null }
    document.getElementById('tr-run').disabled = false;
    document.getElementById('tr-stop').classList.add('hidden');
    if (state === 'ok') setStatus('tr', 'ok', 'Done');
    else if (state === 'err') setStatus('tr', 'err', 'Error');
    else setStatus('tr', '', 'Ready');
}

let digES = null;
let digLines = [];
function runDig() {
    const target = document.getElementById('dig-target').value.trim();
    if (!target) return;
    const qtype = document.getElementById('dig-qtype').value;
    const out = document.getElementById('dig-out');
    if (digES) { digES.close(); digES = null; }
    digLines = [];
    out.innerHTML = '';
    setStatus('dig', 'run', 'Looking up...');
    document.getElementById('dig-run').disabled = true;
    digES = new EventSource('/api/dig?target=' + encodeURIComponent(target) + '&qtype=' + qtype);
    digES.onmessage = e => {
        if (e.data === '[DONE]') {
            if (digES) { digES.close(); digES = null; }
            document.getElementById('dig-run').disabled = false;
            setStatus('dig', 'ok', 'Done');
            renderDNS(out, target, qtype);
            return;
        }
        if (e.data.startsWith('[ERROR]')) {
            out.innerHTML = `<span class="t-err">${esc(e.data.slice(7).trim())}</span>`;
            if (digES) { digES.close(); digES = null; }
            document.getElementById('dig-run').disabled = false;
            setStatus('dig', 'err', 'Error');
            return;
        }
        if (e.data.trim()) digLines.push(e.data);
    };
    digES.onerror = () => {
        if (digES && digES.readyState === 2) {
            if (digES) { digES.close(); digES = null; }
            document.getElementById('dig-run').disabled = false;
            setStatus('dig', 'err', 'Error');
        }
    };
}

function renderDNS(out, target, qtype) {
    out.innerHTML = '';
    if (digLines.length === 0) {
        out.innerHTML = '<span class="ph">No records found</span>';
        return;
    }

    const records = [];
    let summaryLine = '';

    digLines.forEach(line => {
        if (line.includes('=== Summary ===')) return;
        if (line.startsWith('Record found on')) {
            summaryLine = line;
            return;
        }
        if (line.includes(' IN ') && line.includes('(found on')) {
            records.push(line);
        }
    });

    if (records.length === 0) {
        out.innerHTML = `<div class="rcard"><div class="rprefix">No ${esc(qtype)} record found</div></div>`;
        return;
    }

    let html = `<div class="rcard">`;
    html += `<div class="rprefix">${esc(target)} — ${esc(qtype)} Records</div>`;
    html += `<div class="rattrs">`;

    records.forEach(rec => {
        const match = rec.match(/^(.*)\s+\(found on (\d+) resolvers\)$/);
        if (match) {
            const recordValue = match[1].trim();
            const count = match[2];
            html += `
        <div class="rattr wide">
          <div class="rattr-k">Record</div>
          <div class="rattr-v" style="font-family:var(--mono);color:var(--accent)">${esc(recordValue)}</div>
          <div style="margin-top:4px">
            <span class="asn-tag" style="background:var(--blue-dim);border-color:rgba(77,159,255,.3);color:var(--blue)">${count} resolver${count > 1 ? 's' : ''}</span>
          </div>
        </div>`;
        }
    });

    if (summaryLine) {
        html += `
      <div class="rattr wide" style="margin-top:8px;border-top:1px solid var(--bdr);padding-top:8px">
        <div class="rattr-k">Summary</div>
        <div class="rattr-v" style="color:var(--green);font-weight:600">${esc(summaryLine)}</div>
      </div>`;
    }

    html += `</div></div>`;
    out.innerHTML = html;
}

const BGP_PH = { ip: 'e.g. 1.2.3.4', prefix: 'e.g. 1.0.0.0/8', asn: 'e.g. 12880 or AS12880' };
function bgpTypeChanged() { document.getElementById('bgp-query').placeholder = BGP_PH[document.getElementById('bgp-type').value] || '' }
function runBGP() {
    const type = document.getElementById('bgp-type').value;
    const query = document.getElementById('bgp-query').value.trim();
    if (!query) return;
    const out = document.getElementById('bgp-out');
    out.innerHTML = '<span class="ph"><span class="spin"></span>&nbsp;Searching...</span>';
    setStatus('bgp', 'run', 'Searching...');
    fetch('/api/bgp?type=' + encodeURIComponent(type) + '&query=' + encodeURIComponent(query))
        .then(r => r.json())
        .then(data => {
            if (data.error) { setStatus('bgp', 'err', 'Error'); out.innerHTML = `<span class="t-err">${esc(data.error)}</span>`; return }
            const cnt = data.count || 0;
            setStatus('bgp', 'ok', cnt === 0 ? 'No routes found' : cnt + ' route' + (cnt > 1 ? 's' : '') + ' found');
            renderBGP(data, type);
        })
        .catch(err => { setStatus('bgp', 'err', 'Error'); out.innerHTML = `<span class="t-err">Request failed: ${esc(err.message)}</span>` });
}
function originClass(o) { if (!o) return ''; const c = o.toLowerCase(); if (c === 'igp' || c === 'i') return 'origin-i'; if (c === 'egp' || c === 'e') return 'origin-e'; return 'origin-q' }
function renderBGP(data, type) {
    const out = document.getElementById('bgp-out');
    const routes = data.routes || [];
    const aspEnriched = data.aspath_enriched || [];

    if (!routes.length) {
        out.innerHTML = '<span class="ph">No routes found</span>';
        return;
    }

    const max = type === 'asn' ? 50 : routes.length;
    let html = '';

    if (routes.length > max) {
        html += `<div class="notice">Showing ${max} of ${routes.length} routes</div>`;
    }

    for (let i = 0; i < Math.min(routes.length, max); i++) {
        const r = routes[i];

        const orig = r.origin
            ? `<span class="${originClass(r.origin)}">${esc(r.origin)}</span>`
            : '<span style="color:var(--muted)">-</span>';

        let aspHtml = '<span style="color:var(--muted)">-</span>';
        if ((r.aspath || []).length) {
            const enrichMap = {};
            aspEnriched.forEach(a => enrichMap[a.asn] = a);

            aspHtml = (r.aspath || []).map((asn, idx) => {
                const info = enrichMap[asn] || {};
                const name = info.name
                    ? `<span style="color:var(--muted2);font-size:10px;margin-left:3px">${esc(info.name)}</span>`
                    : '';
                const arrow = idx < (r.aspath || []).length - 1
                    ? `<span style="color:var(--bdr2);margin:0 4px">→</span>`
                    : '';
                return `<span class="asn-tag">AS${asn}</span>${name}${arrow}`;
            }).join('');
        }

        const comm = (r.communities || []).length
            ? (r.communities || []).map(c => `<span class="comm-tag">${esc(c)}</span>`).join(' ')
            : '<span style="color:var(--muted)">-</span>';

        let geoHtml = '';
        if (r.geo) {
            const g = r.geo;
            const flag = g.country_code ? getFlagEmoji(g.country_code) : '';
            const parts = [];
            if (g.country) parts.push(`${flag} ${esc(g.country)}`);
            if (g.continent) parts.push(esc(g.continent));
            if (g.as_name) parts.push(`<span style="color:var(--purple)">${esc(g.as_name)}</span>`);
            if (g.as_domain) parts.push(`<span style="color:var(--muted2);font-size:11px">${esc(g.as_domain)}</span>`);
            if (parts.length) {
                geoHtml = `<div class="rattr wide"><div class="rattr-k">Location</div><div class="rattr-v">${parts.join(' · ')}</div></div>`;
            }
        }

        html += `<div class="rcard"><div class="rprefix">${esc(r.prefix)}</div><div class="rattrs">
${geoHtml}
<div class="rattr"><div class="rattr-k">Origin</div><div class="rattr-v">${orig}</div></div>
<div class="rattr"><div class="rattr-k">Local Pref</div><div class="rattr-v">${r.localpref != null ? r.localpref : '-'}</div></div>
<div class="rattr"><div class="rattr-k">MED</div><div class="rattr-v">${r.med != null ? r.med : '-'}</div></div>
<div class="rattr wide"><div class="rattr-k">AS Path</div><div class="rattr-v" style="line-height:2">${aspHtml}</div></div>
<div class="rattr wide"><div class="rattr-k">Communities</div><div class="rattr-v">${comm}</div></div>
</div></div>`;
    }

    out.innerHTML = html;
}
function getFlagEmoji(code) {
    if (!code || code.length !== 2) return '';
    const o = 127397;
    return String.fromCodePoint(code.charCodeAt(0) + o) + String.fromCodePoint(code.charCodeAt(1) + o);
}
document.getElementById('ping-target').addEventListener('keydown', e => { if (e.key === 'Enter') runPing() });
document.getElementById('tr-target').addEventListener('keydown', e => { if (e.key === 'Enter') runTrace() });
document.getElementById('dig-target').addEventListener('keydown', e => { if (e.key === 'Enter') runDig() });
document.getElementById('bgp-query').addEventListener('keydown', e => { if (e.key === 'Enter') runBGP() });

function runSSL() {
    const target = document.getElementById('ssl-target').value.trim();
    if (!target) return;
    const out = document.getElementById('ssl-out');
    out.innerHTML = '<span class="ph"><span class="spin"></span>&nbsp;Checking...</span>';
    setStatus('ssl', 'run', 'Checking...');
    fetch('/api/ssl?target=' + encodeURIComponent(target))
        .then(r => r.json())
        .then(d => {
            if (d.error && !d.subject) { setStatus('ssl', 'err', 'Error'); out.innerHTML = `<span class="t-err">${esc(d.error)}</span>`; return }
            const valid = d.valid;
            setStatus('ssl', valid ? 'ok' : 'err', valid ? 'Valid' : 'Invalid');
            const badge = valid ? `<span style="color:var(--green);font-weight:700">Valid</span>` : `<span style="color:var(--red);font-weight:700">Invalid</span>`;
            const warn = d.error ? `<div class="rattr wide"><div class="rattr-k">Warning</div><div class="rattr-v" style="color:var(--red)">${esc(d.error)}</div></div>` : '';
            const daysColor = d.days_left < 30 ? 'var(--red)' : d.days_left < 60 ? 'var(--yellow)' : 'var(--green)';
            const sans = (d.sans || []).map(s => `<span class="comm-tag">${esc(s)}</span>`).join(' ') || '-';
            out.innerHTML = `<div class="rcard">
<div class="rprefix">${badge} &nbsp;${esc(target)}</div>
<div class="rattrs">
${warn}
<div class="rattr"><div class="rattr-k">Subject</div><div class="rattr-v">${esc(d.subject || '-')}</div></div>
<div class="rattr"><div class="rattr-k">Issuer</div><div class="rattr-v">${esc(d.issuer || '-')}</div></div>
<div class="rattr"><div class="rattr-k">Not Before</div><div class="rattr-v">${esc(d.not_before || '-')}</div></div>
<div class="rattr"><div class="rattr-k">Not After</div><div class="rattr-v">${esc(d.not_after || '-')}</div></div>
<div class="rattr"><div class="rattr-k">Days Left</div><div class="rattr-v" style="color:${daysColor};font-weight:700">${d.days_left ?? '-'}</div></div>
<div class="rattr wide"><div class="rattr-k">SANs</div><div class="rattr-v">${sans}</div></div>
</div></div>`;
        })
        .catch(err => { setStatus('ssl', 'err', 'Error'); out.innerHTML = `<span class="t-err">Request failed: ${esc(err.message)}</span>` });
}
document.getElementById('ssl-target').addEventListener('keydown', e => { if (e.key === 'Enter') runSSL() });

function runPort() {
    const target = document.getElementById('port-target').value.trim();
    const port = document.getElementById('port-port').value.trim();
    const node = document.getElementById('port-node').value;
    if (!target || !port) return;
    const out = document.getElementById('port-out');
    out.innerHTML = '<span class="ph"><span class="spin"></span>&nbsp;Checking...</span>';
    setStatus('port', 'run', 'Checking...');
    fetch('/api/portcheck?node=' + encodeURIComponent(node) + '&target=' + encodeURIComponent(target) + '&port=' + encodeURIComponent(port))
        .then(r => r.json())
        .then(d => {
            if (d.error && !d.status) { setStatus('port', 'err', 'Error'); out.innerHTML = `<span class="t-err">${esc(d.error)}</span>`; return }
            const color = d.status === 'open' ? 'var(--green)' : d.status === 'closed' ? 'var(--red)' : 'var(--yellow)';
            const badge = `<span style="color:${color};font-weight:700;font-size:16px;text-transform:uppercase">${esc(d.status)}</span>`;
            const latency = d.latency_ms != null ? `<div class="rattr"><div class="rattr-k">Latency</div><div class="rattr-v" style="color:var(--green)">${d.latency_ms} ms</div></div>` : '';
            const errRow = d.error ? `<div class="rattr wide"><div class="rattr-k">Detail</div><div class="rattr-v" style="color:var(--muted2)">${esc(d.error)}</div></div>` : '';
            setStatus('port', d.status === 'open' ? 'ok' : 'err', d.status);
            out.innerHTML = `<div class="rcard">
<div class="rprefix">${badge}</div>
<div class="rattrs">
<div class="rattr"><div class="rattr-k">Target</div><div class="rattr-v">${esc(d.target)}</div></div>
<div class="rattr"><div class="rattr-k">Port</div><div class="rattr-v">${esc(String(d.port))}</div></div>
${latency}${errRow}
</div></div>`;
        })
        .catch(err => { setStatus('port', 'err', 'Error'); out.innerHTML = `<span class="t-err">Request failed: ${esc(err.message)}</span>` });
}
document.getElementById('port-target').addEventListener('keydown', e => { if (e.key === 'Enter') runPort() });
document.getElementById('port-port').addEventListener('keydown', e => { if (e.key === 'Enter') runPort() });

function copyShareLink(btn) {
    const activeTab = document.querySelector('.tab.active');
    if (!activeTab) return;
    const tabName = activeTab.id.replace('tab-', '');
    const url = new URL(location.href);
    url.searchParams.set('tab', tabName);
    url.searchParams.set('run', '1');
    let target = '';
    if (tabName === 'dig') {
        target = document.getElementById('dig-target').value.trim();
        const qtype = document.getElementById('dig-qtype').value;
        if (target) {
            url.searchParams.set('t', target);
            url.searchParams.set('q', qtype);
        }
    }
    else if (tabName === 'ssl') {
        target = document.getElementById('ssl-target').value.trim();
        if (target) url.searchParams.set('t', target);
    }
    else if (tabName === 'ping') {
        target = document.getElementById('ping-target').value.trim();
        if (target) url.searchParams.set('t', target);
    }
    else if (tabName === 'traceroute') {
        target = document.getElementById('tr-target').value.trim();
        if (target) {
            url.searchParams.set('t', target);
            const node = document.getElementById('tr-node').value;
            const hops = document.getElementById('tr-hops').value;
            if (node) url.searchParams.set('node', node);
            if (hops) url.searchParams.set('maxhops', hops);
        }
    }
    else if (tabName === 'bgp') {
        target = document.getElementById('bgp-query').value.trim();
        const type = document.getElementById('bgp-type').value;
        if (target) {
            url.searchParams.set('t', target);
            url.searchParams.set('type', type);
        }
    }
    else if (tabName === 'port') {
        target = document.getElementById('port-target').value.trim();
        const port = document.getElementById('port-port').value.trim();
        if (target) {
            url.searchParams.set('t', target);
            if (port) url.searchParams.set('port', port);
            const node = document.getElementById('port-node').value;
            if (node) url.searchParams.set('node', node);
        }
    }
    if (target) {
        navigator.clipboard.writeText(url.toString()).then(() => {
            if (btn) {
                const originalText = btn.textContent;
                btn.textContent = 'Copied!';
                setTimeout(() => {
                    if (btn) btn.textContent = originalText;
                }, 1400);
            }
        });
    }
}

function loadFromURL() {
    const p = new URLSearchParams(location.search);
    const tab = p.get('tab');
    if (tab) switchTab(tab);
    const target = p.get('t') || p.get('target');
    const run = p.get('run') === '1';
    if (tab === 'dig' && target) {
        document.getElementById('dig-target').value = target;
        if (p.get('q')) document.getElementById('dig-qtype').value = p.get('q');
        if (run) setTimeout(runDig, 400);
    }
    if (tab === 'ssl' && target) {
        document.getElementById('ssl-target').value = target;
        if (run) setTimeout(runSSL, 400);
    }
    if (tab === 'ping' && target) {
        document.getElementById('ping-target').value = target;
        if (run) setTimeout(runPingAll, 400);
    }
    if (tab === 'traceroute' && target) {
        document.getElementById('tr-target').value = target;
        if (p.get('node')) document.getElementById('tr-node').value = p.get('node');
        if (p.get('maxhops')) document.getElementById('tr-hops').value = p.get('maxhops');
        if (run) setTimeout(runTrace, 600);
    }
    if (tab === 'bgp' && target) {
        if (p.get('type')) document.getElementById('bgp-type').value = p.get('type');
        document.getElementById('bgp-query').value = target;
        if (run) setTimeout(runBGP, 400);
    }
    if (tab === 'port' && target) {
        document.getElementById('port-target').value = target;
        if (p.get('port')) document.getElementById('port-port').value = p.get('port');
        if (p.get('node')) document.getElementById('port-node').value = p.get('node');
        if (run) setTimeout(runPort, 600);
    }
}

window.addEventListener('popstate', () => loadFromURL());
loadFromURL();
