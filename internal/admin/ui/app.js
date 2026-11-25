"use strict";
// LOTUS admin: sign in, then a read-only dashboard and a Reload button.
// Everything shown is put in with textContent; nothing from the server is parsed as HTML.
(function () {
  var $ = function (id) { return document.getElementById(id); };
  var csrf = "";

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

  function show(signedIn) {
    $("login").hidden = signedIn;
    $("dash").hidden = !signedIn;
    $("logout").hidden = !signedIn;
    $("who").hidden = !signedIn;
  }

  function toast(msg, cls) {
    var t = $("toast");
    t.textContent = msg; t.className = "toast " + (cls || "ok"); t.hidden = false;
    clearTimeout(toast.timer);
    toast.timer = setTimeout(function () { t.hidden = true; }, 6000);
  }

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

  function render(status, health, sites, certs) {
    var cards = $("cards");
    cards.replaceChildren(
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
      return [(c.domains || []).join(", "), c.issuer || "-", c.not_after ? new Date(c.not_after).toLocaleString() : "-", pill(c.status)];
    }), 4, "No certificates.");
  }

  function load() {
    return Promise.all([api("GET", "/v1/status"), api("GET", "/v1/health"), api("GET", "/v1/sites"), api("GET", "/v1/certs")])
      .then(function (r) { render(r[0], r[1], r[2], r[3]); })
      .catch(function (e) {
        if (e.status === 401) { show(false); return; }
        toast(e.message, "bad");
      });
  }

  function enter(sess) {
    csrf = sess.csrf;
    $("who").textContent = sess.user + " (" + sess.role + ")";
    $("reload").hidden = sess.role !== "admin";
    show(true);
    return load();
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
    api("POST", "/v1/logout").catch(function () {}).then(function () { csrf = ""; show(false); });
  });

  $("refresh").addEventListener("click", function () { load().then(function () { toast("Refreshed."); }); });

  $("reload").addEventListener("click", function () {
    var b = $("reload");
    b.disabled = true;
    api("POST", "/v1/reload")
      .then(function (r) { toast("Configuration loaded (generation " + r.generation + ")."); return load(); })
      .catch(function (e) { toast("Not reloaded: " + e.message, "bad"); })
      .then(function () { b.disabled = false; });
  });

  api("GET", "/v1/session").then(enter).catch(function () { show(false); });
})();
