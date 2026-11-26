"use strict";
// LOTUS admin: sign in, a dashboard, and the Sites page (add, edit, delete).
// Everything shown is put in with textContent; nothing from the server is parsed as HTML.
(function () {
  var $ = function (id) { return document.getElementById(id); };
  var csrf = "";
  var role = "";
  var view = "dashboard";
  var editing = null; // the name of the site being edited, or null when adding

  function el(tag, text, cls) {
    var e = document.createElement(tag);
    if (text !== undefined && text !== null) e.textContent = String(text);
    if (cls) e.className = cls;
    return e;
  }

  function api(method, path, body) {
    var headers = { "Accept": "application/json" };
    var opts = { method: method, headers: headers, credentials: "same-origin" };
    if (method !== "GET") headers["X-CSRF-Token"] = csrf;
    if (body !== undefined) { headers["Content-Type"] = "application/json"; opts.body = JSON.stringify(body); }
    return fetch(path, opts).then(function (r) {
      return r.json().catch(function () { return {}; }).then(function (data) {
        if (!r.ok) { var err = new Error(data.message || ("Request failed (" + r.status + ")")); err.status = r.status; err.data = data; throw err; }
        return data;
      });
    });
  }

  function toast(msg, cls) {
    var t = $("toast");
    t.textContent = msg; t.className = "toast " + (cls || "ok"); t.hidden = false;
    clearTimeout(toast.timer);
    toast.timer = setTimeout(function () { t.hidden = true; }, 6000);
  }

  function signedOut() {
    csrf = ""; role = "";
    $("login").hidden = false;
    ["nav", "logout", "who"].forEach(function (id) { $(id).hidden = true; });
    document.querySelectorAll("[data-page]").forEach(function (p) { p.hidden = true; });
  }

  // ---- views ----

  function showView(name) {
    var parts = String(name).split("/"), arg = parts[1] || "";
    name = parts[0];
    view = ["sites", "cache", "php", "phpsettings", "server", "logs", "config", "traffic", "users"].indexOf(name) >= 0 ? name : "dashboard";
    if ((view === "logs" || view === "config" || view === "users") && role !== "admin") view = "dashboard"; // the raw files and logs are for admins
    document.querySelectorAll("[data-page]").forEach(function (p) { p.hidden = p.getAttribute("data-page") !== view; });
    document.querySelectorAll("#nav a").forEach(function (a) {
      var mine = a.getAttribute("data-view") === view || (view === "phpsettings" && a.getAttribute("data-view") === "php");
      if (mine) a.setAttribute("aria-current", "page"); else a.removeAttribute("aria-current");
    });
    var loaders = { traffic: loadTraffic, sites: loadSites, cache: loadCache, php: loadPHP, server: loadServer, logs: loadLogs, config: loadConfig, users: loadUsers, phpsettings: loadPhpSettings };
    return (loaders[view] || loadDashboard)(arg);
  }

  window.addEventListener("hashchange", function () { if (csrf) showView(location.hash.slice(1)); });

  // ---- dashboard ----

  function bytes(n) {
    if (!n) return "0 B";
    var u = ["B", "KiB", "MiB", "GiB", "TiB"], i = 0;
    while (n >= 1024 && i < u.length - 1) { n /= 1024; i++; }
    return (i ? n.toFixed(1) : n) + " " + u[i];
  }

  function fillRows(id, rows, cols, emptyText) {
    var body = $(id);
    body.replaceChildren();
    if (!rows || !rows.length) {
      var tr = el("tr"), td = el("td", emptyText, "empty");
      td.colSpan = cols; tr.appendChild(td); body.appendChild(tr);
      return;
    }
    rows.forEach(function (cells) {
      var tr = el("tr");
      cells.forEach(function (c) {
        var td = el("td");
        if (c instanceof Node) td.appendChild(c); else td.textContent = c;
        tr.appendChild(td);
      });
      body.appendChild(tr);
    });
  }

  function pill(status) {
    var cls = status === "ok" ? "ok" : (status === "expiring" || status === "pending" || status === "degraded") ? "warn" : "bad";
    return el("span", status, "pill " + cls);
  }

  function stat(k, v) {
    var d = el("div", null, "stat");
    d.appendChild(el("div", k, "k")); d.appendChild(el("div", v, "v"));
    return d;
  }

  function renderDashboard(status, health, sites, certs) {
    $("cards").replaceChildren(
      stat("Version", status.version),
      stat("Uptime", status.uptime),
      stat("Sites", status.sites),
      stat("Configuration", "generation " + status.generation),
      stat("Memory", bytes(status.memory_bytes)),
      stat("Goroutines", status.goroutines),
      stat("Connections refused", status.rejected_connections),
      stat("Requests dropped", status.dropped_requests)
    );
    var hl = $("health");
    hl.replaceChildren();
    (health.checks || []).forEach(function (c) {
      var li = el("li");
      li.appendChild(pill(c.status));
      li.appendChild(document.createTextNode(" " + c.name + (c.detail ? ": " + c.detail : "")));
      hl.appendChild(li);
    });
    fillRows("listeners", (status.listeners || []).map(function (l) { return [l.name, l.address, l.https ? "yes" : "no"]; }), 3, "No listeners.");
    fillRows("sites", (sites || []).map(function (s) {
      return [s.name, (s.aliases || []).join(", ") || "-", (s.listeners || []).join(", ") || "-", s.tls || "off"];
    }), 4, "No sites yet.");
    fillRows("certs", (certs || []).map(function (c) {
      var act = el("span");
      if (c.managed && role === "admin") act = renewButton(c);
      return [(c.domains || []).join(", "), c.issuer || "-", c.not_after ? new Date(c.not_after).toLocaleString() : "-", pill(c.status), act];
    }), 5, "No certificates.");
  }

  // A certificate that LOTUS obtains itself (tls automatic) can be renewed here. The name asked for is the managed
  // name of the certificate, not the first domain in the list.
  function renewButton(c) {
    var b = el("button", "Renew", "btn btn-quiet");
    b.type = "button"; b.title = "Renew the certificate of " + c.name; b.setAttribute("aria-label", "Renew the certificate of " + c.name);
    b.addEventListener("click", function () {
      if (!window.confirm("Ask the certificate authority for a new certificate for " + c.name + " now? It can take up to a minute.")) return;
      b.disabled = true; b.textContent = "Renewing...";
      api("POST", "/v1/certs/renew", { domain: c.name })
        .then(function () { toast("Certificate for " + c.name + " renewed."); return loadDashboard(); })
        .catch(function (e) { if (e.status === 401) { signedOut(); return; } toast(e.message, "bad"); b.disabled = false; b.textContent = "Renew"; });
    });
    return b;
  }

  function failed(e) {
    if (e.status === 401) { signedOut(); return; }
    toast(e.message, "bad");
  }

  function loadDashboard() {
    return Promise.all([api("GET", "/v1/status"), api("GET", "/v1/health"), api("GET", "/v1/sites"), api("GET", "/v1/certs")])
      .then(function (r) { renderDashboard(r[0], r[1], r[2], r[3]); })
      .catch(failed);
  }

  // ---- sites ----

  var defaultRoot = "";
  var phpBackends = [];
  var phpLabels = {};
  var listenerNames = [];
  var TEMPLATE_NAMES = { static: "Static files", php: "PHP", wordpress: "WordPress", laravel: "Laravel", proxy: "Reverse proxy" };
  var TEMPLATE_NOTES = {
    static: "Serves the files of the web directory.",
    php: "Runs .php files and sends other URLs to index.php (a front controller).",
    wordpress: "As PHP, and never runs PHP inside wp-content/uploads.",
    laravel: "As PHP, serving the public folder inside the web directory.",
    proxy: "Sends every request to another server (an app on a port). No web directory is needed."
  };

  function loadSites() {
    return api("GET", "/v1/site-files").then(function (d) {
      defaultRoot = d.default_root || "";
      phpBackends = d.php_backends || [];
      phpLabels = d.php_labels || {};
      listenerNames = d.listeners || [];
      var hint = $("site-hint");
      hint.textContent = d.hint || ""; hint.hidden = !d.hint;
      var admin = role === "admin";
      $("site-add").hidden = !admin;
      var body = $("site-rows");
      body.replaceChildren();
      if (!d.entries || !d.entries.length) {
        var tr = el("tr"), td = el("td", "No sites made here yet.", "empty");
        td.colSpan = 7; tr.appendChild(td); body.appendChild(tr);
        return;
      }
      d.entries.forEach(function (s) {
        var tr = el("tr");
        tr.appendChild(el("td", s.name));
        if (!s.editable) {
          var td = el("td", "Written by hand: it is not changed here.", "empty");
          td.colSpan = 5; tr.appendChild(td);
          tr.appendChild(el("td", "-"));
        } else {
          tr.appendChild(el("td", TEMPLATE_NAMES[s.template || "static"] || s.template));
          tr.appendChild(el("td", (s.aliases || []).join(", ") || "-"));
          tr.appendChild(el("td", s.template === "proxy" ? s.proxy_url : s.root));
          tr.appendChild(el("td", (s.index || []).join(", ")));
          tr.appendChild(el("td", s.tls));
          var act = el("td"), box = el("div", null, "row-actions");
          if (admin) {
            var edit = el("button", "Edit", "btn btn-quiet");
            edit.type = "button"; edit.title = "Edit " + s.name; edit.setAttribute("aria-label", "Edit " + s.name);
            edit.addEventListener("click", function () { openForm(s); });
            var del = el("button", "Delete", "btn btn-danger");
            del.type = "button"; del.title = "Delete " + s.name; del.setAttribute("aria-label", "Delete " + s.name);
            del.addEventListener("click", function () { removeSite(s.name); });
            box.appendChild(edit); box.appendChild(del);
          }
          act.appendChild(box); tr.appendChild(act);
        }
        body.appendChild(tr);
      });
    }).catch(failed);
  }

  function showErrors(e) {
    var box = $("site-errors");
    box.replaceChildren();
    box.appendChild(el("div", e.message));
    var list = e.data && e.data.diagnostics;
    if (Array.isArray(list) && list.length) {
      var ul = el("ul");
      list.slice(0, 8).forEach(function (d) {
        ul.appendChild(el("li", (d.where ? d.where + ": " : "") + d.message + (d.hint ? " (" + d.hint + ")" : "")));
      });
      box.appendChild(ul);
    }
    box.hidden = false;
  }

  function openForm(site) {
    editing = site ? site.name : null;
    $("site-form-title").textContent = site ? "Edit " + site.name : "Add a site";
    $("f-name").value = site ? site.name : "";
    $("f-name").readOnly = !!site;
    $("f-aliases").value = site ? (site.aliases || []).join(", ") : "";
    // A new site starts with the web directory made from its name (C:/www/<name>); the box follows the name until
    // the person types in the box themselves. An existing site shows the directory it has.
    if (site) {
      $("f-root").value = site.root;
      $("f-root").removeAttribute("data-auto");
    } else {
      $("f-root").value = autoRoot("");
      $("f-root").setAttribute("data-auto", $("f-root").value);
    }
    $("f-root").placeholder = autoRoot("example.com");
    $("f-index").value = site ? (site.index || []).join(", ") : "index.html";
    $("f-tls").value = site ? site.tls : "off";
    $("f-create").checked = !site;
    fillAdvanced(site || {});
    $("site-errors").hidden = true;
    $("site-form").hidden = false;
    $("f-name").focus();
  }

  function setSel(id, v) { $(id).value = v || ""; }

  function fillAdvanced(s) {
    setSel("f-template", s.template || "static");
    var sel = $("f-php");
    sel.replaceChildren();
    var names = phpBackends.slice();
    if (s.php_backend && names.indexOf(s.php_backend) < 0) names.push(s.php_backend);
    names.forEach(function (n) { var o = el("option", phpLabels[n] || n); o.value = n; sel.appendChild(o); });
    if (!names.length) { var none = el("option", "(no php_backend in the configuration)"); none.value = ""; sel.appendChild(none); }
    setSel("f-php", s.php_backend);
    $("f-proxy").value = s.proxy_url || "";
    $("f-rate").value = s.rate_requests || 0;
    setSel("f-per", s.rate_per || "1s");
    $("f-burst").value = s.rate_burst || 0;
    setSel("f-compression", s.compression);
    setSel("f-headers", s.security_headers);
    setSel("f-symlinks", s.symlinks);
    setSel("f-dotfiles", s.dotfiles);
    $("f-htaccess").checked = !!s.htaccess;
    $("f-listeners").value = (s.listeners || []).join(", ");
    $("f-listeners").placeholder = listenerNames.join(", ");
    var ep = s.error_pages || {};
    $("f-e403").value = ep["403"] || ""; $("f-e404").value = ep["404"] || ""; $("f-e500").value = ep["500"] || "";
    $("f-alog").value = s.access_log || ""; $("f-elog").value = s.error_log || "";
    showTemplate();
  }

  // The fields a type of site needs: PHP types a backend, a proxy an address and no web directory.
  function showTemplate() {
    var t = $("f-template").value, isPHP = /^(php|wordpress|laravel)$/.test(t);
    $("f-template-note").textContent = TEMPLATE_NOTES[t] || "";
    $("f-files").hidden = t === "proxy";
    $("f-root").required = t !== "proxy";
    $("f-php-wrap").hidden = !isPHP;
    $("f-proxy-wrap").hidden = t !== "proxy";
    $("f-proxy").required = t === "proxy";
    $("f-root-wrap").hidden = !!editing || t === "proxy";
    if (!editing && isPHP && $("f-index").value === "index.html") $("f-index").value = "index.php, index.html";
    if (!editing && !isPHP && $("f-index").value === "index.php, index.html") $("f-index").value = "index.html";
  }
  $("f-template").addEventListener("change", showTemplate);

  function advancedBody() {
    var ep = {};
    [["403", "f-e403"], ["404", "f-e404"], ["500", "f-e500"]].forEach(function (p) {
      var v = $(p[1]).value.trim(); if (v) ep[p[0]] = v;
    });
    var t = $("f-template").value;
    return {
      template: t,
      php_backend: $("f-php-wrap").hidden ? "" : $("f-php").value,
      proxy_url: t === "proxy" ? $("f-proxy").value.trim() : "",
      rate_requests: parseInt($("f-rate").value, 10) || 0,
      rate_per: $("f-per").value,
      rate_burst: parseInt($("f-burst").value, 10) || 0,
      compression: $("f-compression").value,
      security_headers: $("f-headers").value,
      symlinks: $("f-symlinks").value,
      dotfiles: $("f-dotfiles").value,
      htaccess: $("f-htaccess").checked,
      listeners: splitList($("f-listeners").value),
      error_pages: ep,
      access_log: $("f-alog").value.trim(),
      error_log: $("f-elog").value.trim()
    };
  }

  function closeForm() { $("site-form").hidden = true; editing = null; }

  function splitList(v) {
    return v.split(",").map(function (s) { return s.trim(); }).filter(function (s) { return s !== ""; });
  }

  $("site-add").addEventListener("click", function () { openForm(null); });
  $("site-cancel").addEventListener("click", closeForm);

  // The web directory of a new site: the default base (the part of default_root before NAME, C:/www on Windows)
  // plus the site name as the folder. Only letters, digits, dot and dash are kept, as the server requires of a name.
  function autoRoot(name) {
    var base = (defaultRoot || "C:/www/NAME").replace(/NAME$/, "").replace(/\/+$/, "");
    return base + "/" + name.trim().toLowerCase().replace(/[^a-z0-9.-]/g, "");
  }

  // Typing a new name rebuilds the web directory, until the person changes that field.
  $("f-name").addEventListener("input", function () {
    if (editing) return;
    var root = $("f-root");
    if (root.value === "" || root.value === root.getAttribute("data-auto")) {
      var v = autoRoot($("f-name").value);
      root.value = v; root.setAttribute("data-auto", v);
    }
  });

  $("site-form").addEventListener("submit", function (ev) {
    ev.preventDefault();
    var btn = $("site-save");
    var body = advancedBody();
    body.name = $("f-name").value.trim(); body.aliases = splitList($("f-aliases").value);
    body.root = $("f-template").value === "proxy" ? "" : $("f-root").value.trim();
    body.index = splitList($("f-index").value); body.tls = $("f-tls").value;
    var req;
    if (editing) {
      delete body.name;
      req = api("PUT", "/v1/site-files/" + encodeURIComponent(editing), body);
    } else {
      body.create_root = $("f-create").checked;
      req = api("POST", "/v1/site-files", body);
    }
    btn.disabled = true; $("site-errors").hidden = true;
    req.then(function () {
      toast(editing ? "Site saved and loaded." : "Site added and loaded.");
      closeForm();
      return loadSites();
    }).catch(function (e) {
      if (e.status === 401) { signedOut(); return; }
      showErrors(e);
    }).then(function () { btn.disabled = false; });
  });

  function removeSite(name) {
    if (!window.confirm("Delete the site " + name + "? Its web directory is not touched.")) return;
    api("DELETE", "/v1/site-files/" + encodeURIComponent(name))
      .then(function () { toast("Site " + name + " deleted."); return loadSites(); })
      .catch(failed);
  }

  // ---- live traffic ----

  var trafficTimer = null;
  var samples = [];      // the last readings: {t: seconds, total, bytes, errors, sites: {name: {requests, bytes}}, raw}
  var MAX_SAMPLES = 60;  // 3 minutes at one reading every 3 seconds
  var SVGNS = "http://www.w3.org/2000/svg";

  function rate(a, b, key) { var dt = b.t - a.t; return dt > 0 ? Math.max(0, (b[key] - a[key]) / dt) : 0; }

  function fmtRate(n) { return n >= 100 ? Math.round(n).toString() : n.toFixed(n >= 10 ? 1 : 2); }

  function drawChart(id, values, format) {
    var svg = $(id);
    svg.replaceChildren();
    if (values.length < 2) return;
    var max = Math.max.apply(null, values.concat([0.0001])), w = 300, h = 70, pts = [];
    values.forEach(function (v, i) { pts.push((i * w / (MAX_SAMPLES - 1)).toFixed(1) + "," + (h - 4 - (v / max) * (h - 10)).toFixed(1)); });
    var last = pts[pts.length - 1].split(",")[0];
    var area = document.createElementNS(SVGNS, "polygon");
    area.setAttribute("class", "area"); area.setAttribute("points", "0," + h + " " + pts.join(" ") + " " + last + "," + h);
    var line = document.createElementNS(SVGNS, "polyline");
    line.setAttribute("class", "line"); line.setAttribute("points", pts.join(" "));
    svg.appendChild(area); svg.appendChild(line);
    var cap = svg.parentNode.querySelector("figcaption .now");
    if (!cap) { cap = el("span", "", "now"); svg.parentNode.querySelector("figcaption").appendChild(cap); }
    cap.textContent = "  " + format(values[values.length - 1]) + " now, peak " + format(max);
  }

  function renderTraffic() {
    if (samples.length < 1) return;
    var cur = samples[samples.length - 1], prev = samples[samples.length - 2] || cur;
    var reqRates = [], byteRates = [], errRates = [];
    for (var i = 1; i < samples.length; i++) {
      reqRates.push(rate(samples[i - 1], samples[i], "total"));
      byteRates.push(rate(samples[i - 1], samples[i], "bytes"));
      errRates.push(rate(samples[i - 1], samples[i], "errors"));
    }
    drawChart("chart-req", reqRates, function (v) { return fmtRate(v) + "/s"; });
    drawChart("chart-bytes", byteRates, function (v) { return bytes(v) + "/s"; });
    drawChart("chart-err", errRates, function (v) { return fmtRate(v) + "/s"; });

    var raw = cur.raw;
    var errShare = cur.total > 0 ? (cur.errors / cur.total * 100) : 0;
    $("traffic-cards").replaceChildren(
      stat("Requests per second", fmtRate(rate(prev, cur, "total"))),
      stat("Data sent per second", bytes(rate(prev, cur, "bytes")) + "/s"),
      stat("Open connections", raw.connections + " of " + raw.connection_limit),
      stat("Errors since start", errShare.toFixed(1) + "% of " + cur.total),
      stat("Refused connections", raw.rejected),
      stat("Dropped requests", raw.dropped)
    );

    var rows = [];
    var all = (raw.sites || []).concat([raw.unrouted]);
    all.forEach(function (s) {
      if (s.name === "" && s.requests === 0) return;
      var name = s.name === "" ? "(no site matched)" : s.name;
      var p = prev.sites[s.name] || { requests: s.requests, bytes: s.bytes };
      var dt = cur.t - prev.t;
      var rps = dt > 0 ? Math.max(0, (s.requests - p.requests) / dt) : 0;
      var bps = dt > 0 ? Math.max(0, (s.bytes - p.bytes) / dt) : 0;
      var avg = s.requests > 0 ? (s.latency_seconds / s.requests * 1000).toFixed(1) + " ms" : "-";
      var c = s.class || [];
      rows.push([name, fmtRate(rps), bytes(bps) + "/s", c[1] || 0, c[2] || 0, c[3] || 0, c[4] || 0, avg, s.rate_limited || 0]);
    });
    fillRows("traffic-rows", rows, 9, "No sites yet.");
    document.querySelectorAll("#traffic-rows td:not(:first-child)").forEach(function (td) { td.classList.add("num"); });
  }

  function readTraffic() {
    return api("GET", "/v1/traffic").then(function (d) {
      var errors = 0, total = 0, byteSum = 0, map = {};
      (d.sites || []).concat([d.unrouted]).forEach(function (s) {
        var c = s.class || [];
        total += s.requests; byteSum += s.bytes; errors += (c[3] || 0) + (c[4] || 0);
        map[s.name] = { requests: s.requests, bytes: s.bytes };
      });
      samples.push({ t: d.time_ms / 1000, total: total, bytes: byteSum, errors: errors, sites: map, raw: d });
      if (samples.length > MAX_SAMPLES) samples.shift();
      $("traffic-state").textContent = "Updating every 3 seconds";
      renderTraffic();
    }).catch(function (e) {
      if (e.status === 401) { stopTraffic(); signedOut(); return; }
      $("traffic-state").textContent = "Could not read the figures: " + e.message;
    });
  }

  function stopTraffic() { if (trafficTimer) { clearInterval(trafficTimer); trafficTimer = null; } }

  function loadTraffic() {
    stopTraffic();
    samples = [];
    var first = readTraffic();
    // Poll only while this page is open and the browser tab is visible.
    trafficTimer = setInterval(function () {
      if (view !== "traffic" || !csrf) { stopTraffic(); return; }
      if (!document.hidden) readTraffic();
    }, 3000);
    return first;
  }

  // ---- cache ----

  function loadCache() {
    return api("GET", "/v1/cache").then(function (zones) {
      fillRows("cache-rows", (zones || []).map(function (z) {
        return [z.zone, z.entries, bytes(z.bytes) + " of " + bytes(z.max_bytes), z.hits, z.misses, z.stale, z.evictions, z.purged];
      }), 8, "No cache zones. Add a cache block to the configuration.");
      var sel = $("p-zone");
      sel.replaceChildren();
      (zones || []).forEach(function (z) { var o = el("option", z.zone); o.value = z.zone; sel.appendChild(o); });
      $("purge-form").hidden = role !== "admin" || !zones || !zones.length;
    }).catch(failed);
  }

  function syncPurgeForm() { $("p-path-wrap").hidden = $("p-what").value === "all"; }
  $("p-what").addEventListener("change", syncPurgeForm);
  $("cache-refresh").addEventListener("click", function () { loadCache(); });

  $("purge-form").addEventListener("submit", function (ev) {
    ev.preventDefault();
    var what = $("p-what").value, body = { zone: $("p-zone").value, host: $("p-host").value.trim() };
    if (what === "all") body.all = true; else body[what] = $("p-path").value.trim();
    if (what === "all" && !window.confirm("Remove everything stored in the zone " + body.zone + "?")) return;
    var btn = $("purge-go"), box = $("purge-errors");
    btn.disabled = true; box.hidden = true;
    api("POST", "/v1/cache/purge", body)
      .then(function (r) { toast(r.purged + " stored response(s) removed."); return loadCache(); })
      .catch(function (e) {
        if (e.status === 401) { signedOut(); return; }
        box.textContent = e.message; box.hidden = false;
      })
      .then(function () { btn.disabled = false; });
  });

  // ---- php ----

  var poolEditing = null;
  var poolList = [];
  var phpInstalled = [];
  var jobTimer = null;
  var jobMakePool = false;

  // ---- php versions ----

  function loadPHPVersions() {
    var admin = role === "admin";
    return api("GET", "/v1/php-versions").then(function (d) {
      var rt = $("pv-runtime");
      rt.replaceChildren();
      if (d.supported && !d.runtime_ok) {
        rt.appendChild(el("div", d.runtime_message));
        var a = el("a", "Download the Visual C++ Redistributable (x64)");
        a.href = "https://aka.ms/vs/17/release/vc_redist.x64.exe"; a.rel = "noopener noreferrer"; a.target = "_blank";
        rt.appendChild(a);
      }
      rt.hidden = !(d.supported && !d.runtime_ok);
      $("pv-unsupported").hidden = d.supported;
      var pkg = d.kind === "packages";
      $("pv-avail-head").textContent = pkg ? "On this server, or from the package manager" : "Available from windows.php.net";
      $("pv-note-zip").hidden = pkg; $("pv-note-pkg").hidden = !pkg;
      var feed = $("pv-feed");
      feed.textContent = d.feed_error ? "Could not read the list of versions: " + d.feed_error : "";
      feed.hidden = !d.feed_error;

      phpInstalled = d.installed || [];
      fillRows("pv-installed", phpInstalled.map(function (v) {
        var box = el("div", null, "row-actions");
        var settings = el("button", "Settings", "btn btn-quiet");
        settings.type = "button"; settings.setAttribute("aria-label", "Settings and extensions of PHP " + v.version);
        settings.addEventListener("click", function () { location.hash = "#phpsettings/" + encodeURIComponent(v.version); });
        box.appendChild(settings);
        if (admin) {
          var mk = el("button", "Make pool", "btn btn-quiet");
          mk.type = "button"; mk.setAttribute("aria-label", "Make a pool for PHP " + v.version);
          mk.addEventListener("click", function () {
            act_(mk, api("POST", "/v1/php-versions/" + encodeURIComponent(v.version) + "/pool", {}), "Pool made.", loadPHP);
          });
          var rm = el("button", "Remove", "btn btn-danger");
          rm.type = "button"; rm.setAttribute("aria-label", "Remove PHP " + v.version);
          rm.addEventListener("click", function () {
            if (!window.confirm(pkg ? "Stop using PHP " + v.version + "? The system's package stays installed." : "Remove PHP " + v.version + "? Its folder is deleted.")) return;
            act_(rm, api("DELETE", "/v1/php-versions/" + encodeURIComponent(v.version)), "PHP " + v.version + " removed.", loadPHPVersions);
          });
          box.appendChild(mk); box.appendChild(rm);
        }
        return ["PHP " + v.version, (v.pools || []).join(", ") || "-", v.dir, box];
      }), 4, d.supported ? "No PHP installed here yet. Install one below." : "-");

      fillRows("pv-available", (d.available || []).map(function (a) {
        var cell;
        if (a.installed) cell = el("span", "Installed", "pill ok");
        else if (!admin || !d.runtime_ok) cell = el("span", "-");
        else {
          cell = el("button", a.found ? "Use" : "Install", "btn");
          cell.type = "button"; cell.setAttribute("aria-label", (a.found ? "Use PHP " : "Install PHP ") + a.version);
          if (!a.found && !d.can_install) {
            cell.disabled = true;
            cell.title = "The LOTUS service does not run as root, so it can't install packages. Install the package yourself, then press Refresh.";
          }
          cell.addEventListener("click", function () { startInstall(a.version, cell); });
        }
        return ["PHP " + a.branch, a.version, a.size ? Math.round(a.size / 1048576) + " MB" : "-", cell];
      }), 4, d.supported ? "No list of versions." : "-");
      $("pv-mkpool").parentNode.hidden = !d.supported || !admin;

      var fills = $("pl-fills");
      fills.querySelectorAll("[data-version]").forEach(function (b) { b.remove(); });
      phpInstalled.forEach(function (v) {
        var b = el("button", "PHP " + v.version, "btn btn-quiet");
        b.type = "button"; b.setAttribute("data-version", v.version);
        b.addEventListener("click", function () { $("pl-command").value = v.exe.replace(/\\/g, "/"); $("pl-ini").value = v.ini.replace(/\\/g, "/"); });
        fills.appendChild(b);
      });
      if (d.job && d.job.state === "running") watchJob(d.job.id, "pv", afterInstall);
    }).catch(failed);
  }

  function startInstall(version, btn) {
    jobMakePool = $("pv-mkpool").checked;
    btn.disabled = true;
    api("POST", "/v1/php-versions", { version: version }).then(function (r) {
      watchJob(r.job, "pv", afterInstall);
    }).catch(function (e) {
      if (e.status === 401) { signedOut(); return; }
      toast(e.message, "bad");
    }).then(function () { btn.disabled = false; });
  }

  // After an install: make the pool if it was asked for, then show the pools.
  function afterInstall(j) {
    var wants = jobMakePool && j.label.indexOf("Installing PHP") === 0;
    var after = wants
      ? api("POST", "/v1/php-versions/" + encodeURIComponent(j.version) + "/pool", {}).then(function () { toast("Pool made."); }, function (e) { toast(e.message, "bad"); })
      : Promise.resolve();
    return after.then(function () { jobMakePool = false; return loadPHP(); });
  }

  // Polls a background job every second until it ends, and shows its progress in the box with this id prefix.
  function watchJob(id, prefix, onDone) {
    if (jobTimer) clearInterval(jobTimer);
    var box = $(prefix + "-job");
    box.hidden = false;
    var tick = function () {
      api("GET", "/v1/php-jobs/" + encodeURIComponent(id)).then(function (j) {
        $(prefix + "-job-title").textContent = j.state === "failed" ? j.label + ": failed" : j.label;
        $(prefix + "-progress").value = j.state === "failed" ? 0 : j.percent;
        $(prefix + "-job-text").textContent = j.message;
        if (j.state === "running") return;
        clearInterval(jobTimer); jobTimer = null;
        if (j.state === "done") { toast(j.message + "."); box.hidden = true; return onDone(j); }
      }).catch(function (e) {
        clearInterval(jobTimer); jobTimer = null;
        if (e.status === 401) { signedOut(); return; }
        $(prefix + "-job-text").textContent = e.message;
      });
    };
    tick();
    jobTimer = setInterval(tick, 1000);
  }

  // ---- php settings page (#phpsettings/8.3.37) ----

  var iniVersion = null;
  var iniExtsEditable = true;

  function loadPhpSettings(version) {
    if (!version) { location.hash = "#php"; return Promise.resolve(); }
    iniVersion = version;
    $("ini-title").textContent = "PHP " + version + ": settings";
    var admin = role === "admin", base = "/v1/php-versions/" + encodeURIComponent(version);
    return Promise.all([api("GET", base + "/ini"), api("GET", base + "/ioncube")]).then(function (r) {
      var d = r[0];
      var fields = $("ini-fields");
      fields.replaceChildren();
      var group = "", grid = null, current = d.settings || {};
      (d.setting_info || []).forEach(function (s) {
        if (s.group !== group || !grid) {
          group = s.group;
          fields.appendChild(el("h3", group, "ini-group"));
          grid = el("div", null, "grid2 two");
          fields.appendChild(grid);
        }
        var wrap = el("div", null, "field"), lab = el("label", s.label), input;
        var raw = current[s.key], has = raw !== undefined;
        if (s.type === "switch") {
          // php.ini spells a switch many ways (1, on, true, yes); show them as On or Off.
          var val = has ? (/^(1|on|true|yes)$/i.test(raw) ? "On" : "Off") : "";
          input = el("select");
          (has ? [] : [["", "Not set (PHP default)"]]).concat([["On", "On"], ["Off", "Off"]]).forEach(function (v) { var o = el("option", v[1]); o.value = v[0]; input.appendChild(o); });
          input.value = val;
          input.setAttribute("data-orig", val);
        } else {
          input = el("input"); input.type = "text"; input.maxLength = 1000; input.spellcheck = false;
          input.value = has ? raw : "";
          input.setAttribute("data-orig", input.value);
        }
        input.id = "ini-" + s.key.replace(/[^a-z0-9]/g, "-"); input.setAttribute("data-key", s.key);
        lab.htmlFor = input.id;
        input.disabled = !admin;
        wrap.appendChild(lab); wrap.appendChild(input); wrap.appendChild(el("span", s.hint, "muted field-hint"));
        grid.appendChild(wrap);
      });
      var exts = $("ini-exts");
      exts.replaceChildren();
      iniExtsEditable = d.extensions_editable !== false;
      $("ini-ext-note").hidden = iniExtsEditable;
      (d.available || []).forEach(function (n) {
        var lab = el("label", null, "check"), cb = el("input");
        cb.type = "checkbox"; cb.checked = (d.enabled || []).indexOf(n) >= 0; cb.setAttribute("data-ext", n);
        cb.disabled = !admin || !iniExtsEditable;
        lab.appendChild(cb); lab.appendChild(document.createTextNode(" " + n)); exts.appendChild(lab);
      });
      $("ini-save").hidden = !admin;
      $("ini-ext-save").hidden = !admin || !iniExtsEditable;
      $("ini-errors").hidden = true;
      renderIon(r[1]);
    }).catch(failed);
  }

  function saveIni(settings, extensions, btn) {
    btn.disabled = true; $("ini-errors").hidden = true;
    api("PUT", "/v1/php-versions/" + encodeURIComponent(iniVersion) + "/ini", { settings: settings, extensions: extensions }).then(function (r) {
      toast("Saved." + ((r.restarted || []).length ? " Pool " + r.restarted.join(", ") + " is restarting." : ""));
      return loadPhpSettings(iniVersion);
    }).catch(function (e) {
      if (e.status === 401) { signedOut(); return; }
      var box = $("ini-errors"); box.textContent = e.message; box.hidden = false;
    }).then(function () { btn.disabled = false; });
  }

  $("ini-form").addEventListener("submit", function (ev) {
    ev.preventDefault();
    var settings = {};
    $("ini-fields").querySelectorAll("[data-key]").forEach(function (i) { if (i.value !== i.getAttribute("data-orig")) settings[i.getAttribute("data-key")] = i.value; });
    if (!Object.keys(settings).length) { toast("Nothing changed."); return; }
    saveIni(settings, {}, $("ini-save"));
  });

  $("ini-ext-save").addEventListener("click", function () {
    var extensions = {};
    $("ini-exts").querySelectorAll("[data-ext]").forEach(function (i) { if (!i.disabled) extensions[i.getAttribute("data-ext")] = i.checked; });
    saveIni({}, extensions, $("ini-ext-save"));
  });

  // ionCube Loader card
  function renderIon(st) {
    var admin = role === "admin";
    $("ic-state").textContent = !st.available ? st.message : st.enabled ? "Enabled: PHP loads the ionCube Loader." : st.downloaded ? "Downloaded, but not enabled." : "Not installed.";
    $("ic-install").hidden = !(admin && st.available && !st.downloaded);
    $("ic-enable").hidden = !(admin && st.available && st.downloaded && !st.enabled);
    $("ic-disable").hidden = !(admin && st.available && st.enabled);
  }

  function refreshIon() {
    return api("GET", "/v1/php-versions/" + encodeURIComponent(iniVersion) + "/ioncube").then(renderIon).catch(failed);
  }

  function ionAction(action, btn) {
    btn.disabled = true;
    api("POST", "/v1/php-versions/" + encodeURIComponent(iniVersion) + "/ioncube", { action: action }).then(function (r) {
      if (r.job) { watchJob(r.job, "ic", refreshIon); return; }
      toast("ionCube Loader " + (action === "enable" ? "enabled." : "disabled.") + ((r.restarted || []).length ? " Pool " + r.restarted.join(", ") + " is restarting." : ""));
      return refreshIon();
    }).catch(function (e) {
      if (e.status === 401) { signedOut(); return; }
      toast(e.message, "bad");
    }).then(function () { btn.disabled = false; });
  }
  $("ic-install").addEventListener("click", function () { ionAction("install", $("ic-install")); });
  $("ic-enable").addEventListener("click", function () { ionAction("enable", $("ic-enable")); });
  $("ic-disable").addEventListener("click", function () { ionAction("disable", $("ic-disable")); });

  function loadPHP() {
    var admin = role === "admin";
    loadPHPVersions();
    $("pool-add").hidden = !admin;
    return Promise.all([api("GET", "/v1/php"), api("GET", "/v1/php-pools")]).then(function (r) {
      var pools = r[0] || [], set = r[1], canRestart = set.can_restart && admin;
      fillRows("php-rows", pools.map(function (p) {
        var state = p.alive === p.processes ? el("span", p.alive + " of " + p.processes, "pill ok") : el("span", p.alive + " of " + p.processes, "pill warn");
        var act = el("div", null, "row-actions");
        if (canRestart) {
          var b = el("button", "Restart", "btn btn-quiet");
          b.type = "button"; b.setAttribute("aria-label", "Restart the pool " + p.name);
          b.addEventListener("click", function () {
            act_(b, api("POST", "/v1/php/" + encodeURIComponent(p.name) + "/restart"), "Pool " + p.name + " is restarting.", loadPHP);
          });
          act.appendChild(b);
        }
        return [p.name, p.command, p.processes, state, p.restarts, act];
      }), 6, "No managed PHP pools are running.");

      poolList = set.pools || [];
      var note = $("pool-note");
      note.textContent = set.editable ? "" : "The file sites.d/00-lotus-server.conf was changed by hand, so pools can't be changed here.";
      note.hidden = set.editable;
      $("pool-add").disabled = !set.editable;
      fillRows("pool-rows", poolList.map(function (p) {
        var box = el("div", null, "row-actions");
        if (admin && set.editable) {
          var edit = el("button", "Edit", "btn btn-quiet");
          edit.type = "button"; edit.setAttribute("aria-label", "Edit the pool " + p.name);
          edit.addEventListener("click", function () { openPoolForm(p); });
          var del = el("button", "Delete", "btn btn-danger");
          del.type = "button"; del.setAttribute("aria-label", "Delete the pool " + p.name);
          del.addEventListener("click", function () { deletePool(p.name); });
          box.appendChild(edit); box.appendChild(del);
        }
        return [p.name, p.command, p.processes, p.max_requests ? p.max_requests + " requests" : "never", p.timeout + " s", box];
      }), 6, "No pools made here yet.");
    }).catch(failed);
  }
  $("php-refresh").addEventListener("click", function () { loadPHP(); });

  // Runs a request on a button, shows a toast, and calls then() when it worked.
  function act_(btn, request, okText, then) {
    btn.disabled = true;
    request.then(function () { toast(okText); if (then) return then(); })
      .catch(function (e) { if (e.status === 401) { signedOut(); return; } toast(e.message, "bad"); })
      .then(function () { btn.disabled = false; });
  }

  function openPoolForm(p) {
    poolEditing = p ? p.name : null;
    $("pool-form-title").textContent = p ? "Edit the pool " + p.name : "Add a pool";
    $("pl-name").value = p ? p.name : "app"; $("pl-name").readOnly = !!p;
    $("pl-command").value = p ? p.command : "";
    $("pl-processes").value = p ? p.processes : 4;
    $("pl-max").value = p ? (p.max_requests || 0) : 500;
    $("pl-timeout").value = p ? p.timeout : 60;
    $("pl-ini").value = p ? (p.php_ini || "") : "";
    $("pool-errors").hidden = true;
    $("pool-form").hidden = false;
    $("pl-command").focus();
  }
  $("pool-add").addEventListener("click", function () { openPoolForm(null); });
  $("pool-cancel").addEventListener("click", function () { $("pool-form").hidden = true; poolEditing = null; });
  $("pl-xampp").addEventListener("click", function () { $("pl-command").value = "C:/xampp/php/php-cgi.exe"; });
  $("pl-path").addEventListener("click", function () { $("pl-command").value = "php-cgi"; });

  $("pool-form").addEventListener("submit", function (ev) {
    ev.preventDefault();
    var btn = $("pool-save"), name = $("pl-name").value.trim();
    var body = {
      command: $("pl-command").value.trim(), processes: parseInt($("pl-processes").value, 10) || 0,
      max_requests: parseInt($("pl-max").value, 10) || 0, timeout: parseInt($("pl-timeout").value, 10) || 0, php_ini: $("pl-ini").value.trim()
    };
    btn.disabled = true; $("pool-errors").hidden = true;
    api("PUT", "/v1/php-pools/" + encodeURIComponent(name), body).then(function () {
      toast("Pool saved and loaded.");
      $("pool-form").hidden = true; poolEditing = null;
      return loadPHP();
    }).catch(function (e) {
      if (e.status === 401) { signedOut(); return; }
      var box = $("pool-errors"); box.replaceChildren();
      box.appendChild(el("div", e.message));
      var list = e.data && e.data.diagnostics;
      if (Array.isArray(list)) list.slice(0, 6).forEach(function (d) { box.appendChild(el("div", (d.where ? d.where + ": " : "") + d.message)); });
      box.hidden = false;
    }).then(function () { btn.disabled = false; });
  });

  function deletePool(name) {
    if (!window.confirm("Delete the pool " + name + "? Sites that use it must be changed first.")) return;
    api("DELETE", "/v1/php-pools/" + encodeURIComponent(name))
      .then(function () { toast("Pool " + name + " deleted."); return loadPHP(); })
      .catch(function (e) { if (e.status === 401) { signedOut(); return; } toast(e.message, "bad"); });
  }

  // ---- logs ----

  function loadLogs() {
    return api("GET", "/v1/logs").then(function (list) {
      var sel = $("l-source"), keep = sel.value;
      sel.replaceChildren();
      (list || []).forEach(function (s) { var o = el("option", s.name); o.value = s.id; sel.appendChild(o); });
      if (keep) sel.value = keep;
      if (!list || !list.length) {
        $("logs-view").textContent = "";
        showLogNote("No log files are configured for the admin to show.");
        return;
      }
      return showLog();
    }).catch(failed);
  }

  function showLogNote(text) { var n = $("logs-note"); n.textContent = text || ""; n.hidden = !text; }

  function showLog() {
    var id = $("l-source").value;
    if (!id) return Promise.resolve();
    var q = "?lines=" + encodeURIComponent($("l-lines").value) + "&filter=" + encodeURIComponent($("l-filter").value.trim());
    return api("GET", "/v1/logs/" + encodeURIComponent(id) + q).then(function (d) {
      var view = $("logs-view");
      view.textContent = (d.lines || []).join("\n");
      showLogNote(d.note || ((d.lines || []).length ? "" : "No lines match."));
      view.scrollTop = view.scrollHeight;
    }).catch(failed);
  }

  $("logs-refresh").addEventListener("click", function () { showLog(); });
  $("l-source").addEventListener("change", showLog);
  $("l-lines").addEventListener("change", showLog);
  $("l-filter").addEventListener("keydown", function (ev) { if (ev.key === "Enter") { ev.preventDefault(); showLog(); } });
  $("l-filter").addEventListener("change", showLog);

  // ---- configuration editor ----

  var cfgOriginal = "";
  var cfgFiles = [];

  function loadConfig() {
    return api("GET", "/v1/config/files").then(function (d) {
      cfgFiles = d.files || [];
      var sel = $("c-file"), keep = sel.value;
      sel.replaceChildren();
      cfgFiles.forEach(function (f) { var o = el("option", f.name); o.value = f.name; sel.appendChild(o); });
      if (keep && cfgFiles.some(function (f) { return f.name === keep; })) sel.value = keep;
      return openConfigFile();
    }).catch(failed);
  }

  function openConfigFile() {
    var name = $("c-file").value;
    $("c-result").hidden = true;
    var f = cfgFiles.filter(function (x) { return x.name === name; })[0];
    $("c-managed").hidden = !(f && f.managed);
    if (!name) { $("c-text").value = ""; $("c-history").replaceChildren(); return Promise.resolve(); }
    return api("GET", "/v1/config/file?name=" + encodeURIComponent(name)).then(function (d) {
      cfgOriginal = d.content; $("c-text").value = d.content;
      return loadHistory();
    }).catch(failed);
  }

  function loadHistory() {
    var name = $("c-file").value;
    return api("GET", "/v1/config/history?name=" + encodeURIComponent(name)).then(function (d) {
      var body = $("c-history");
      body.replaceChildren();
      if (!d.versions || !d.versions.length) {
        var tr = el("tr"), td = el("td", "No earlier versions yet.", "empty");
        td.colSpan = 4; tr.appendChild(td); body.appendChild(tr);
        return;
      }
      d.versions.forEach(function (v) {
        var tr = el("tr");
        tr.appendChild(el("td", new Date(v.time).toLocaleString()));
        tr.appendChild(el("td", v.who));
        tr.appendChild(el("td", v.size + " bytes"));
        var act = el("td"), btn = el("button", "Load into editor", "btn btn-quiet");
        btn.type = "button"; btn.setAttribute("aria-label", "Load the version of " + new Date(v.time).toLocaleString() + " into the editor");
        btn.addEventListener("click", function () { loadVersion(name, v); });
        act.appendChild(btn); tr.appendChild(act); body.appendChild(tr);
      });
    }).catch(failed);
  }

  function loadVersion(name, v) {
    if ($("c-text").value !== cfgOriginal && !window.confirm("Replace your unsaved changes in the editor with this version?")) return;
    api("GET", "/v1/config/version?name=" + encodeURIComponent(name) + "&id=" + encodeURIComponent(v.id)).then(function (d) {
      $("c-text").value = d.content;
      showCfgResult(true, {}, "Version of " + new Date(v.time).toLocaleString() + " is in the editor. Press Check, then Save and reload, to use it.");
    }).catch(failed);
  }

  function showCfgResult(ok, e, okText) {
    var box = $("c-result");
    box.replaceChildren();
    box.className = ok ? "success" : "error";
    box.appendChild(el("div", ok ? okText : e.message));
    var list = ok ? e.diagnostics : (e.data && e.data.diagnostics);
    if (Array.isArray(list) && list.length) {
      var ul = el("ul");
      list.slice(0, 12).forEach(function (d) {
        ul.appendChild(el("li", (d.where ? d.where + ": " : "") + d.message + (d.hint ? " (" + d.hint + ")" : "")));
      });
      box.appendChild(ul);
    }
    box.hidden = false;
  }

  function cfgBody() { return { name: $("c-file").value, content: $("c-text").value }; }

  $("c-file").addEventListener("change", function () {
    if ($("c-text").value !== cfgOriginal && !window.confirm("Discard your changes to this file?")) { return; }
    openConfigFile();
  });

  $("c-revert").addEventListener("click", function () { $("c-text").value = cfgOriginal; $("c-result").hidden = true; });

  $("c-validate").addEventListener("click", function () {
    var b = $("c-validate"); b.disabled = true;
    api("POST", "/v1/config/validate", cfgBody())
      .then(function (r) { showCfgResult(true, r, "The configuration is valid with this file."); })
      .catch(function (e) { if (e.status === 401) { signedOut(); return; } showCfgResult(false, e); })
      .then(function () { b.disabled = false; });
  });

  $("c-save").addEventListener("click", function () {
    var b = $("c-save"); b.disabled = true;
    api("PUT", "/v1/config/file", cfgBody())
      .then(function (r) {
        cfgOriginal = $("c-text").value;
        showCfgResult(true, {}, "Saved and loaded (generation " + r.generation + ").");
        toast("Configuration saved and loaded.");
        loadHistory();
      })
      .catch(function (e) { if (e.status === 401) { signedOut(); return; } showCfgResult(false, e); })
      .then(function () { b.disabled = false; });
  });

  // Tab inserts a tab in the editor, not a move to the next control (Escape then Tab leaves it).
  $("c-text").addEventListener("keydown", function (ev) {
    if (ev.key === "Tab" && !ev.shiftKey && !ev.ctrlKey && !ev.altKey && !ev.metaKey && !ev.defaultPrevented && ev.target.dataset.tabs !== "off") {
      ev.preventDefault();
      var t = ev.target, s = t.selectionStart;
      t.value = t.value.slice(0, s) + "    " + t.value.slice(t.selectionEnd);
      t.selectionStart = t.selectionEnd = s + 4;
    } else if (ev.key === "Escape") {
      ev.target.dataset.tabs = "off";
    }
  });
  $("c-text").addEventListener("blur", function (ev) { delete ev.target.dataset.tabs; });

  window.addEventListener("beforeunload", function (ev) {
    if (csrf && $("c-text").value !== cfgOriginal) { ev.preventDefault(); ev.returnValue = ""; }
  });

  // ---- users ----

  var userEditing = null;

  function loadUsers() {
    return api("GET", "/v1/users").then(function (list) {
      var body = $("user-rows");
      body.replaceChildren();
      list.forEach(function (u) {
        var tr = el("tr");
        tr.appendChild(el("td", u.name + (u.self ? " (you)" : "")));
        tr.appendChild(el("td", u.role));
        var act = el("td"), box = el("div", null, "row-actions");
        var edit = el("button", "Change", "btn btn-quiet");
        edit.type = "button"; edit.setAttribute("aria-label", "Change the account " + u.name);
        edit.addEventListener("click", function () { openUserForm(u); });
        box.appendChild(edit);
        if (!u.self) {
          var del = el("button", "Delete", "btn btn-danger");
          del.type = "button"; del.setAttribute("aria-label", "Delete the account " + u.name);
          del.addEventListener("click", function () { deleteUser(u.name); });
          box.appendChild(del);
        }
        act.appendChild(box); tr.appendChild(act); body.appendChild(tr);
      });
    }).catch(failed);
  }

  function openUserForm(u) {
    userEditing = u ? u.name : null;
    $("user-form-title").textContent = u ? "Change " + u.name : "Add an account";
    $("u-name").value = u ? u.name : ""; $("u-name").readOnly = !!u;
    $("u-role").value = u ? u.role : "viewer";
    $("u-role").disabled = !!(u && u.self); // your own role is changed by another admin
    $("u-pass").value = ""; $("u-current").value = "";
    $("u-pass").required = !u;
    $("u-pass-label").textContent = u ? "New password (leave empty to keep the old one)" : "Password";
    $("u-current-wrap").hidden = !(u && u.self);
    $("user-errors").hidden = true;
    $("user-form").hidden = false;
    (u ? $("u-pass") : $("u-name")).focus();
  }

  function closeUserForm() { $("user-form").hidden = true; $("u-pass").value = ""; $("u-current").value = ""; userEditing = null; }

  $("user-add").addEventListener("click", function () { openUserForm(null); });
  $("user-cancel").addEventListener("click", closeUserForm);

  $("user-form").addEventListener("submit", function (ev) {
    ev.preventDefault();
    var btn = $("user-save"), req;
    if (userEditing) {
      var body = {};
      if ($("u-pass").value) { body.password = $("u-pass").value; if (!$("u-current-wrap").hidden) body.current_password = $("u-current").value; }
      if (!$("u-role").disabled) body.role = $("u-role").value;
      req = api("PUT", "/v1/users/" + encodeURIComponent(userEditing), body);
    } else {
      req = api("POST", "/v1/users", { name: $("u-name").value.trim(), role: $("u-role").value, password: $("u-pass").value });
    }
    btn.disabled = true; $("user-errors").hidden = true;
    req.then(function () {
      toast(userEditing ? "Account changed." : "Account added.");
      closeUserForm();
      return loadUsers();
    }).catch(function (e) {
      if (e.status === 401) { signedOut(); return; }
      var box = $("user-errors"); box.textContent = e.message; box.hidden = false;
    }).then(function () { btn.disabled = false; });
  });

  function deleteUser(name) {
    if (!window.confirm("Delete the account " + name + "? It is signed out at once.")) return;
    api("DELETE", "/v1/users/" + encodeURIComponent(name))
      .then(function () { toast("Account " + name + " deleted."); return loadUsers(); })
      .catch(function (e) { if (e.status === 401) { signedOut(); return; } toast(e.message, "bad"); });
  }

  // ---- server ----

  function loadServer() {
    var admin = role === "admin";
    ["server-reload", "server-logs"].forEach(function (id) { $(id).disabled = !admin; });
    $("https-card").hidden = !admin;
    if (!admin) return Promise.resolve();
    return api("GET", "/v1/https").then(function (d) {
      var state = $("https-state"), s = d.spec;
      $("https-after").hidden = true;
      var listeners = (d.listeners || []).map(function (l) { return l.name + " " + l.address + (l.https ? " (HTTPS)" : ""); }).join(", ") || "none";
      if (s) {
        state.textContent = "Set up here: certificates are requested automatically (" + (s.staging ? "test authority" : "real authority") + "). Listeners in the files: " + listeners + ".";
        $("h-email").value = s.email; $("h-https").value = s.https;
        $("h-http-on").checked = !!s.http; $("h-staging").checked = !!s.staging;
      } else if (d.acme) {
        state.textContent = "Automatic certificates are already set up in main.conf (contact " + d.acme_email + "). Listeners: " + listeners + ". Use the Configuration editor to change them.";
      } else {
        state.textContent = "Not set up. Listeners in the files: " + listeners + ".";
      }
      var blocked = !d.editable || (!s && d.acme);
      ["h-email", "h-https", "h-http-on", "h-staging", "https-save"].forEach(function (id) { $(id).disabled = blocked; });
      $("https-remove").hidden = !s;
      if (!d.editable) state.textContent += " The file sites.d/00-lotus-server.conf was changed by hand, so it can't be changed here.";
    }).catch(failed);
  }

  $("https-form").addEventListener("submit", function (ev) {
    ev.preventDefault();
    var btn = $("https-save");
    var body = { email: $("h-email").value.trim(), https: $("h-https").value.trim(), http: $("h-http-on").checked ? "0.0.0.0:80" : "", staging: $("h-staging").checked };
    btn.disabled = true; $("https-errors").hidden = true; $("https-after").hidden = true;
    api("PUT", "/v1/https", body).then(function (r) {
      if (r.restart_required) showHTTPSAfter("Saved. The listeners change only when the service restarts: restart LOTUS (Windows: Start Menu, Restart LOTUS, or Restart-Service LOTUS in an Administrator shell; Linux: systemctl restart lotus). The admin page is unavailable for a moment.");
      else showHTTPSAfter("Saved and loaded.");
      return loadServer();
    }).catch(function (e) {
      if (e.status === 401) { signedOut(); return; }
      var box = $("https-errors"); box.replaceChildren();
      box.appendChild(el("div", e.message));
      var list = e.data && e.data.diagnostics;
      if (Array.isArray(list)) list.slice(0, 6).forEach(function (d) { box.appendChild(el("div", (d.where ? d.where + ": " : "") + d.message)); });
      box.hidden = false;
    }).then(function () { btn.disabled = false; });
  });

  $("https-remove").addEventListener("click", function () {
    if (!window.confirm("Remove the HTTPS setup made here? The listeners go away after the next restart.")) return;
    api("DELETE", "/v1/https").then(function (r) {
      showHTTPSAfter(r.restart_required ? "Removed. Restart the LOTUS service to close the listeners." : "Removed.");
      return loadServer();
    }).catch(function (e) { if (e.status === 401) { signedOut(); return; } toast(e.message, "bad"); });
  });

  function showHTTPSAfter(text) { var n = $("https-after"); n.textContent = text; n.hidden = false; }

  function act(btn, request, okText) {
    btn.disabled = true;
    request.then(function (r) { toast(typeof okText === "function" ? okText(r) : okText); })
      .catch(function (e) { if (e.status === 401) { signedOut(); return; } toast(e.message, "bad"); })
      .then(function () { btn.disabled = false; });
  }

  $("server-reload").addEventListener("click", function () {
    act($("server-reload"), api("POST", "/v1/reload"), function (r) { return "Configuration loaded (generation " + r.generation + ")."; });
  });
  $("server-logs").addEventListener("click", function () {
    act($("server-logs"), api("POST", "/v1/logs/reopen"), function (r) { return r.reopened + " log file(s) reopened."; });
  });

  // ---- sign in and out ----

  function enter(sess) {
    csrf = sess.csrf; role = sess.role;
    $("who").textContent = sess.user + " (" + sess.role + ")";
    $("reload").hidden = sess.role !== "admin";
    document.querySelectorAll("#nav [data-admin]").forEach(function (a) { a.hidden = sess.role !== "admin"; });
    $("login").hidden = true;
    ["nav", "logout", "who"].forEach(function (id) { $(id).hidden = false; });
    return showView(location.hash.slice(1));
  }

  $("login-form").addEventListener("submit", function (ev) {
    ev.preventDefault();
    var btn = ev.target.querySelector("button"), err = $("login-error");
    btn.disabled = true; err.hidden = true;
    api("POST", "/v1/login", { name: $("name").value, password: $("password").value })
      .then(function (sess) { $("password").value = ""; return enter(sess); })
      .catch(function (e) { err.textContent = e.message; err.hidden = false; })
      .then(function () { btn.disabled = false; });
  });

  $("logout").addEventListener("click", function () {
    api("POST", "/v1/logout").catch(function () {}).then(signedOut);
  });

  $("refresh").addEventListener("click", function () { loadDashboard().then(function () { toast("Refreshed."); }); });

  $("reload").addEventListener("click", function () {
    var b = $("reload");
    b.disabled = true;
    api("POST", "/v1/reload")
      .then(function (r) { toast("Configuration loaded (generation " + r.generation + ")."); return loadDashboard(); })
      .catch(function (e) { toast("Not reloaded: " + e.message, "bad"); })
      .then(function () { b.disabled = false; });
  });

  api("GET", "/v1/session").then(enter).catch(signedOut);
})();
