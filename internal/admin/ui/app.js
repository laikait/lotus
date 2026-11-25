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
    view = name === "sites" ? "sites" : "dashboard";
    document.querySelectorAll("[data-page]").forEach(function (p) { p.hidden = p.getAttribute("data-page") !== view; });
    document.querySelectorAll("#nav a").forEach(function (a) {
      if (a.getAttribute("data-view") === view) a.setAttribute("aria-current", "page"); else a.removeAttribute("aria-current");
    });
    return view === "sites" ? loadSites() : loadDashboard();
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
      return [(c.domains || []).join(", "), c.issuer || "-", c.not_after ? new Date(c.not_after).toLocaleString() : "-", pill(c.status)];
    }), 4, "No certificates.");
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

  function loadSites() {
    return api("GET", "/v1/site-files").then(function (d) {
      defaultRoot = d.default_root || "";
      var hint = $("site-hint");
      hint.textContent = d.hint || ""; hint.hidden = !d.hint;
      var admin = role === "admin";
      $("site-add").hidden = !admin;
      var body = $("site-rows");
      body.replaceChildren();
      if (!d.entries || !d.entries.length) {
        var tr = el("tr"), td = el("td", "No sites made here yet.", "empty");
        td.colSpan = 6; tr.appendChild(td); body.appendChild(tr);
        return;
      }
      d.entries.forEach(function (s) {
        var tr = el("tr");
        tr.appendChild(el("td", s.name));
        if (!s.editable) {
          var td = el("td", "Written by hand: it is not changed here.", "empty");
          td.colSpan = 4; tr.appendChild(td);
          tr.appendChild(el("td", "-"));
        } else {
          tr.appendChild(el("td", (s.aliases || []).join(", ") || "-"));
          tr.appendChild(el("td", s.root));
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
    $("f-root").value = site ? site.root : defaultRoot;
    $("f-root").placeholder = defaultRoot;
    $("f-index").value = site ? (site.index || []).join(", ") : "index.html";
    $("f-tls").value = site ? site.tls : "off";
    $("f-create").checked = !site;
    $("f-root-wrap").hidden = !!site;
    $("site-errors").hidden = true;
    $("site-form").hidden = false;
    $("f-name").focus();
  }

  function closeForm() { $("site-form").hidden = true; editing = null; }

  function splitList(v) {
    return v.split(",").map(function (s) { return s.trim(); }).filter(function (s) { return s !== ""; });
  }

  $("site-add").addEventListener("click", function () { openForm(null); });
  $("site-cancel").addEventListener("click", closeForm);

  // Typing a new name fills the default web directory, until the person changes that field.
  $("f-name").addEventListener("input", function () {
    if (editing || !defaultRoot) return;
    var root = $("f-root");
    if (root.value === "" || root.value === root.getAttribute("data-auto")) {
      var v = defaultRoot.replace("NAME", $("f-name").value.trim().toLowerCase() || "NAME");
      root.value = v; root.setAttribute("data-auto", v);
    }
  });

  $("site-form").addEventListener("submit", function (ev) {
    ev.preventDefault();
    var btn = $("site-save");
    var body = {
      name: $("f-name").value.trim(), aliases: splitList($("f-aliases").value), root: $("f-root").value.trim(),
      index: splitList($("f-index").value), tls: $("f-tls").value
    };
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

  // ---- sign in and out ----

  function enter(sess) {
    csrf = sess.csrf; role = sess.role;
    $("who").textContent = sess.user + " (" + sess.role + ")";
    $("reload").hidden = sess.role !== "admin";
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
