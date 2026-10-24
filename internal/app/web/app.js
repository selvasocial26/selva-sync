/* Selva Sync app window. Plain JavaScript, no external libraries. */
(function () {
  "use strict";
  const TOKEN = document.querySelector('meta[name="selva-token"]').content;
  const $ = (s, el) => (el || document).querySelector(s);
  const $$ = (s, el) => Array.from((el || document).querySelectorAll(s));

  let L = {};            // strings for current language
  let lang = document.documentElement.lang || "en";
  let S = null;          // full state from /api/state
  let logSeq = 0;
  let logs = [];
  let page = "home";
  let saveTimer = null;
  let dirty = false;
  let editVer = 0;
  let credsDirty = false;
  let ssidNow = "";

  // ---------- helpers ----------
  function t(key, args) {
    let s = L[key] !== undefined ? L[key] : key;
    if (args) for (const k in args) s = s.split("{" + k + "}").join(args[k]);
    return s;
  }
  function el(tag, attrs, ...kids) {
    const e = document.createElement(tag);
    if (attrs) for (const k in attrs) {
      if (k === "class") e.className = attrs[k];
      else if (k === "text") e.textContent = attrs[k];
      else if (k.startsWith("on")) e.addEventListener(k.slice(2), attrs[k]);
      else e.setAttribute(k, attrs[k]);
    }
    for (const c of kids) if (c != null) e.append(c);
    return e;
  }
  async function api(path, body) {
    const opt = { headers: { "X-Selva-Token": TOKEN } };
    if (body !== undefined) {
      opt.method = "POST";
      opt.headers["Content-Type"] = "application/json";
      opt.body = JSON.stringify(body);
    }
    let r;
    try { r = await fetch(path, opt); }
    catch (_) { setOffline(true); throw new Error("err_offline"); }
    setOffline(false);
    let data = null;
    try { data = await r.json(); } catch (_) { }
    if (!r.ok) throw new Error((data && data.error) || ("HTTP " + r.status));
    return data;
  }
  let offline = false;
  function setOffline(v) {
    if (v === offline) return;
    offline = v;
    const b = $("#offlineBar");
    if (b) b.classList.toggle("hidden", !v);
  }
  function toast(msg) {
    const tEl = $("#toast");
    tEl.textContent = msg;
    tEl.classList.remove("hidden");
    clearTimeout(toast._t);
    toast._t = setTimeout(() => tEl.classList.add("hidden"), 3200);
  }
  function bytes(n) {
    n = Number(n) || 0;
    const u = ["B", "KB", "MB", "GB", "TB"];
    let i = 0;
    while (n >= 1024 && i < u.length - 1) { n /= 1024; i++; }
    return num(n, i === 0 ? 0 : (n < 10 ? 1 : 0)) + " " + u[i];
  }
  function num(n, d) {
    try { return new Intl.NumberFormat(lang, { maximumFractionDigits: d || 0, minimumFractionDigits: d || 0 }).format(n); }
    catch (_) { return n.toFixed(d || 0); }
  }
  function fmtDate(v, withDay) {
    if (!v) return "—";
    const d = new Date(v);
    if (isNaN(d) || d.getFullYear() < 2000) return "—";
    const opts = { hour: "2-digit", minute: "2-digit" };
    const today = new Date();
    const sameDay = d.toDateString() === today.toDateString();
    const tom = new Date(today); tom.setDate(today.getDate() + 1);
    const yes = new Date(today); yes.setDate(today.getDate() - 1);
    let day = "";
    if (sameDay) day = t("today");
    else if (d.toDateString() === tom.toDateString()) day = t("tomorrow");
    else if (d.toDateString() === yes.toDateString()) day = t("yesterday");
    else day = d.toLocaleDateString(lang, { day: "numeric", month: "short" });
    let time;
    try { time = d.toLocaleTimeString(lang, opts); } catch (_) { time = d.toLocaleTimeString(); }
    return withDay === false ? time : day + ", " + time;
  }
  function confirmBox(title, text) {
    return new Promise((res) => {
      $("#confirmTitle").textContent = title;
      $("#confirmText").textContent = text;
      const m = $("#confirmModal");
      m.classList.remove("hidden");
      const done = (v) => { m.classList.add("hidden"); $("#confirmYes").onclick = $("#confirmNo").onclick = null; res(v); };
      $("#confirmYes").onclick = () => done(true);
      $("#confirmNo").onclick = () => done(false);
    });
  }

  // ---------- language ----------
  async function loadLang(code) {
    L = await api("/i18n/" + encodeURIComponent(code) + ".json");
    lang = code;
    document.documentElement.lang = code;
    document.documentElement.dir = L._dir === "rtl" ? "rtl" : "ltr";
    applyI18n();
  }
  function applyI18n() {
    $$("[data-i18n]").forEach((e) => { e.textContent = t(e.dataset.i18n); });
    $$("[data-i18n-ph]").forEach((e) => { e.placeholder = t(e.dataset.i18nPh); });
    const wd = $("#weekdaySel");
    const cur = wd.value;
    wd.innerHTML = "";
    for (let i = 0; i < 7; i++) wd.append(el("option", { value: i, text: t("weekday_" + i) }));
    wd.value = cur || "0";
    if (S) renderAll();
  }
  function fillLangPickers() {
    for (const id of ["#langPick", "#langPick2"]) {
      const sel = $(id);
      sel.innerHTML = "";
      for (const l of S.languages) sel.append(el("option", { value: l.code, text: l.name + (l.user ? " *" : "") }));
      sel.value = S.settings.language;
    }
  }
  async function changeLang(code) {
    S.settings.language = code;
    await loadLang(code);
    await saveNow();
    fillLangPickers();
  }

  // ---------- settings binding ----------
  function getPath(obj, path) { return path.split(".").reduce((o, k) => (o ? o[k] : undefined), obj); }
  function setPath(obj, path, val) {
    const ks = path.split(".");
    let o = obj;
    for (let i = 0; i < ks.length - 1; i++) o = o[ks[i]];
    o[ks[ks.length - 1]] = val;
  }
  function bindInputs() {
    $$("[data-set]").forEach((inp) => {
      const path = inp.dataset.set;
      const ev = inp.type === "text" || inp.type === "number" ? "input" : "change";
      inp.addEventListener(ev, () => {
        let v;
        if (inp.type === "checkbox") v = inp.checked;
        else if (inp.type === "radio") { if (!inp.checked) return; v = inp.value; }
        else v = inp.value;
        if (inp.dataset.type === "int") v = parseInt(v, 10) || 0;
        setPath(S.settings, path, v);
        scheduleSave();
        renderDerived();
      });
    });
  }
  function fillInputs() {
    $$("[data-set]").forEach((inp) => {
      if (document.activeElement === inp) return;
      const v = getPath(S.settings, inp.dataset.set);
      if (inp.type === "checkbox") inp.checked = !!v;
      else if (inp.type === "radio") inp.checked = String(v) === inp.value;
      else inp.value = v == null ? "" : v;
    });
    if (document.activeElement !== $("#excludes")) $("#excludes").value = (S.settings.excludes || []).join("\n");
    if (document.activeElement !== $("#capGB")) $("#capGB").value = S.settings.monthlyCapMB ? +(S.settings.monthlyCapMB / 1024).toFixed(2) : 0;
    if (!credsDirty && document.activeElement !== $("#clientId") && document.activeElement !== $("#clientSecret")) {
      $("#clientId").value = S.settings.clientId || "";
      $("#clientSecret").value = "";
      $("#clientSecret").placeholder = S.secretSet ? t("secret_saved_ph") : "";
    }
  }
  function scheduleSave() {
    dirty = true;
    editVer++;
    clearTimeout(saveTimer);
    saveTimer = setTimeout(saveNow, 600);
  }
  async function saveNow() {
    clearTimeout(saveTimer);
    const ver = editVer;
    try {
      const r = await api("/api/settings", S.settings);
      if (ver === editVer) {
        // nothing was edited while saving: take the server's cleaned copy
        dirty = false;
        const focused = document.activeElement;
        S.settings = r.settings;
        S.secretSet = r.secretSet;
        if (!focused || !focused.dataset || !focused.dataset.set) fillInputs();
        renderSources();
      } else {
        scheduleSave(); // newer edits: save again
      }
      toast(t("saved"));
    } catch (e) { toast(t(e.message === "err_offline" ? "err_offline" : "save_failed")); }
  }

  // ---------- navigation ----------
  function show(p) {
    page = p;
    $$(".page").forEach((s) => s.classList.toggle("active", s.id === "page-" + p));
    $$("#nav button").forEach((b) => b.classList.toggle("active", b.dataset.page === p));
    if (p === "internet") refreshNetwork();
    if (p === "activity") renderLogs();
    $("#main").scrollTop = 0;
  }

  // ---------- rendering ----------
  function renderAll() {
    fillInputs();
    renderHome();
    renderSources();
    renderDerived();
    renderChips();
    renderAccountSettings();
    renderLogs();
    $("#verText").textContent = S.version;
    $("#dataDir").textContent = S.dataDir;
  }

  function statusInfo() {
    const st = S.status, lr = S.lastRun;
    if (!S.configured) return { cls: "warn", title: t("st_setup_needed"), sub: t("st_setup_needed_d") };
    if (!S.signedIn) return { cls: "idle", title: t("st_not_signed_in"), sub: t("st_not_signed_in_d") };
    if (st.running) {
      const ph = { prepare: "ph_prepare", connect: "ph_connect", check: "ph_check", scan: "ph_scan", upload: "ph_upload", done: "ph_upload" }[st.phase] || "ph_upload";
      return { cls: "run", title: st.dryRun ? t("st_previewing") : t("st_syncing"), sub: t(ph) };
    }
    if (!S.settings.sources.some((s) => s.enabled)) return { cls: "idle", title: t("st_no_folders"), sub: t("st_no_folders_d") };
    if (!lr) return { cls: "idle", title: t("st_ready"), sub: t("st_ready_d") };
    switch (lr.result) {
      case "ok": case "nothing": return { cls: "", title: t("st_ok"), sub: t("st_ok_d", { time: fmtDate(lr.end) }) };
      case "partial": return { cls: "warn", title: t("st_partial"), sub: t("st_partial_d", { n: lr.failed }) };
      case "cap": return { cls: "warn", title: t("st_cap"), sub: t("st_cap_d") };
      case "blocked": return { cls: "warn", title: t("st_blocked"), sub: t("reason_" + lr.reason), force: true };
      case "stopped": return { cls: "idle", title: t("st_stopped"), sub: t("st_stopped_d") };
      case "failed":
        if (lr.reason === "signin") return { cls: "bad", title: t("st_signin"), sub: t("st_signin_d") };
        if (lr.reason === "drive_full") return { cls: "bad", title: t("st_full"), sub: t("st_full_d") };
        return { cls: "bad", title: t("st_failed"), sub: t("st_failed_d") };
    }
    return { cls: "idle", title: t("st_ready"), sub: "" };
  }

  function renderHome() {
    // setup notice
    const box = $("#setupNotice");
    box.innerHTML = "";
    const hasFolders = S.settings.sources.some((s) => s.enabled);
    if (S.loadStatus === "recovered" || S.loadStatus === "reset") {
      box.append(el("div", { class: "notice warn" },
        el("h3", { text: t("settings_" + S.loadStatus + "_title") }),
        el("p", { text: t("settings_" + S.loadStatus + "_d") })));
    }
    if (!S.configured) {
      box.append(el("div", { class: "notice warn" },
        el("h3", { text: t("setup_google_title") }),
        el("p", { text: t("setup_google_d") }),
        el("div", { class: "actions" }, el("button", { class: "btn primary", text: t("btn_open_advanced"), onclick: () => { show("settings"); $("#advanced").open = true; } }))));
    } else if (!S.signedIn || !hasFolders) {
      const steps = el("ol", { class: "steps" },
        el("li", { class: S.signedIn ? "done" : "", text: t("step_connect") }),
        el("li", { class: hasFolders ? "done" : "", text: t("step_folders") }),
        el("li", { text: t("step_schedule") }));
      const btns = el("div", { class: "actions" });
      if (!S.signedIn) btns.append(el("button", { class: "btn primary big", text: t("btn_connect_google"), onclick: signIn }));
      else btns.append(el("button", { class: "btn primary big", text: t("btn_add_folder"), onclick: () => { show("folders"); openPicker(); } }));
      box.append(el("div", { class: "notice info" }, el("h3", { text: t("welcome_title") }), el("p", { text: t("welcome_d") }), steps, btns,
        S.signedIn ? null : el("p", { class: "hint", text: t("connect_privacy") })));
    }

    const st = S.status;
    const info = statusInfo();
    $("#statusDot").className = "status-dot " + info.cls;
    $("#statusTitle").textContent = info.title;
    $("#statusSub").textContent = info.sub;
    const ready = S.configured && S.signedIn && hasFolders;
    $("#btnSync").classList.toggle("hidden", st.running);
    $("#btnPreview").classList.toggle("hidden", st.running);
    $("#btnSync").disabled = $("#btnPreview").disabled = !ready;
    $("#btnStop").classList.toggle("hidden", !st.running);
    $("#btnForce").classList.toggle("hidden", st.running || !info.force);

    const pb = $("#progressBox");
    pb.classList.toggle("hidden", !(st.running && st.phase === "upload"));
    if (st.running) {
      const pct = st.bytesTotal > 0 ? Math.min(100, (st.bytesDone / st.bytesTotal) * 100) : (st.filesTotal ? st.filesDone / st.filesTotal * 100 : 0);
      $("#barFill").style.width = pct.toFixed(1) + "%";
      $("#progFiles").textContent = t("prog_files", { done: num(st.filesDone), total: num(st.filesTotal) });
      $("#progBytes").textContent = t("prog_bytes", { done: bytes(st.bytesDone), total: bytes(st.bytesTotal) });
      $("#progSpeed").textContent = st.speedBps > 0 ? bytes(st.speedBps) + "/s" : "";
      $("#progCurrent").textContent = st.current || "";
    }

    // tiles
    const lr = S.lastRun;
    $("#tileLast").textContent = S.lastSuccess ? fmtDate(S.lastSuccess) : t("never");
    $("#tileLastSub").textContent = lr && !lr.dryRun ? t("last_summary", { up: num(lr.uploaded + lr.updated), ar: num(lr.archived), sz: bytes(lr.bytes) }) : "";
    if (S.settings.schedule.mode === "manual") { $("#tileNext").textContent = t("manual_only"); $("#tileNextSub").textContent = ""; }
    else {
      const due = S.nextRun && new Date(S.nextRun) <= new Date();
      $("#tileNext").textContent = due ? t("due_now") : fmtDate(S.nextRun);
      $("#tileNextSub").textContent = schedText();
    }
    const cap = S.settings.monthlyCapMB * 1048576;
    $("#tileData").textContent = bytes(S.monthUsage);
    $("#tileDataSub").textContent = cap ? t("of_cap", { cap: bytes(cap) }) : t("this_month");
    const acc = S.account;
    if (acc && acc.limit > 0) {
      const p = acc.usage / acc.limit * 100;
      $("#tileDrive").textContent = num(p, 0) + "%";
      $("#tileDriveSub").textContent = t("drive_used", { used: bytes(acc.usage), total: bytes(acc.limit) });
      const b = $("#driveBar"); b.style.width = Math.min(100, p) + "%"; b.className = p > 95 ? "bad" : p > 85 ? "warn" : "";
    } else if (acc) {
      $("#tileDrive").textContent = bytes(acc.usage); $("#tileDriveSub").textContent = t("unlimited");
    } else { $("#tileDrive").textContent = "—"; $("#tileDriveSub").textContent = ""; }

    // account card
    const ac = $("#accountCard");
    ac.innerHTML = "";
    if (S.signedIn) {
      const email = acc ? acc.email : "";
      ac.append(el("div", { class: "avatar", text: (email || "G").charAt(0).toUpperCase() }),
        el("div", { class: "grow" }, el("div", { text: t("connected_as") }), el("b", { text: email || "…" })),
        el("button", { class: "btn", text: t("btn_open_drive"), onclick: () => api("/api/open", { what: "drive" }) }));
      ac.classList.remove("hidden");
    } else ac.classList.add("hidden");
  }

  function schedText() {
    const s = S.settings.schedule;
    switch (s.mode) {
      case "interval": return t("sched_desc_interval", { n: s.everyHours });
      case "daily": return t("sched_desc_daily", { time: s.time });
      case "weekly": return t("sched_desc_weekly", { day: t("weekday_" + s.weekday), time: s.time });
    }
    return t("manual_only");
  }

  function remotePrefix(p) {
    let m = /^([A-Za-z]):[\\/]?(.*)$/.exec(p);
    let head, rest;
    if (m) { head = m[1].toUpperCase() + "-Drive"; rest = m[2]; }
    else if (p.startsWith("\\\\")) { const parts = p.slice(2).split(/[\\/]/); head = "Network-" + parts.slice(0, 2).join("-"); rest = parts.slice(2).join("/"); }
    else { head = "Root"; rest = p; }
    const segs = rest.split(/[\\/]+/).filter(Boolean);
    return [head].concat(segs).join("/");
  }

  function renderSources() {
    const list = $("#sourceList");
    list.innerHTML = "";
    const srcs = S.settings.sources;
    if (!srcs.length) { list.append(el("div", { class: "empty", text: t("no_folders_yet") })); return; }
    srcs.forEach((s, i) => {
      const isDrive = /^[A-Za-z]:[\\/]?$/.test(s.path);
      const tog = el("label", { class: "toggle", title: t("enabled") });
      const cb = el("input", { type: "checkbox" });
      cb.checked = s.enabled;
      const sid = s.id, spath = s.path;
      const find = () => S.settings.sources.find((x) => (sid ? x.id === sid : x.path === spath));
      cb.addEventListener("change", () => { const cur = find(); if (cur) { cur.enabled = cb.checked; scheduleSave(); renderHome(); } });
      tog.append(cb, el("span"));
      const pathLine = el("div", { class: "path", text: s.path });
      if (isDrive) pathLine.append(el("span", { class: "tag", text: t("whole_drive") }));
      const changed = (S.diskChanged || []).includes(s.path);
      if (changed) {
        pathLine.append(el("span", { class: "tag", text: t("src_new_disk") }));
        pathLine.append(el("button", { class: "btn small", style: "margin-inline-start:8px", text: t("btn_use_disk"), onclick: async () => {
          if (!(await confirmBox(t("btn_use_disk"), t("use_disk_d", { path: s.path })))) return;
          await api("/api/accept-disk", { path: s.path });
          await refreshState();
        } }));
      }
      list.append(el("div", { class: "source" }, tog,
        el("div", { class: "grow" }, pathLine,
          el("div", { class: "remote", text: "→ Google Drive / " + S.settings.driveFolderName + " / " + S.settings.computerName + " / " + remotePrefix(s.path) })),
        el("button", { class: "btn small ghost", text: t("btn_remove"), onclick: async () => {
          if (!(await confirmBox(t("remove_folder_title"), t("remove_folder_d", { path: s.path })))) return;
          const cur = find();
          S.settings.sources = S.settings.sources.filter((x) => x !== cur);
          dirty = true; editVer++;
          await saveNow(); renderSources(); renderHome();
        } })));
    });
  }

  function speedHint(kb) {
    if (!kb) return t("no_limit");
    return t("speed_hint", { gb: num(kb * 3600 / 1048576, 1) });
  }
  function renderDerived() {
    $("#dayHint").textContent = speedHint(S.settings.dayLimitKBps);
    $("#nightHint").textContent = speedHint(S.settings.nightLimitKBps);
    const cap = S.settings.monthlyCapMB * 1048576;
    const used = S.monthUsage || 0;
    const bar = $("#capBar");
    if (cap) {
      const p = used / cap * 100;
      bar.style.width = Math.min(100, p) + "%"; bar.className = p >= 100 ? "bad" : p > 85 ? "warn" : "";
      $("#capUsed").textContent = t("cap_used", { used: bytes(used), cap: bytes(cap) });
    } else { bar.style.width = "0"; $("#capUsed").textContent = t("cap_used_nolimit", { used: bytes(used) }); }
    $("#pathDemo").textContent = "Google Drive / " + S.settings.driveFolderName + " / " + S.settings.computerName + " / D-Drive / …";
    $("#schedNext").textContent = S.settings.schedule.mode === "manual" ? t("manual_only") : t("next_sync_at", { time: fmtDate(S.nextRun) });
  }

  function renderChips() {
    for (const [key, box] of [["blockedWifi", "#blockedChips"], ["allowedWifi", "#allowedChips"]]) {
      const c = $(box);
      c.innerHTML = "";
      (S.settings[key] || []).forEach((name, i) => {
        c.append(el("span", { class: "chip" }, name, el("button", { "aria-label": t("btn_remove"), text: "×", onclick: () => { S.settings[key].splice(i, 1); scheduleSave(); renderChips(); } })));
      });
    }
  }
  function addChip(key, inputSel, value) {
    const v = (value !== undefined ? value : $(inputSel).value).trim();
    if (!v) return;
    S.settings[key] = S.settings[key] || [];
    if (!S.settings[key].some((x) => x.toLowerCase() === v.toLowerCase())) S.settings[key].push(v);
    if (inputSel) $(inputSel).value = "";
    scheduleSave();
    renderChips();
  }

  function renderAccountSettings() {
    const c = $("#accountSettings");
    c.innerHTML = "";
    c.append(el("h3", { text: t("account_title") }));
    if (S.signedIn) {
      c.append(el("p", { text: t("connected_as") + " " + (S.account ? S.account.email : "…") }),
        el("p", { class: "hint", text: t("permission_note") }),
        el("div", { class: "row-actions" }, el("button", { class: "btn danger", text: t("btn_sign_out"), onclick: signOut })));
    } else {
      c.append(el("p", { class: "muted", text: t("not_connected") }),
        el("div", { class: "row-actions" }, el("button", { class: "btn primary", text: t("btn_connect_google"), onclick: signIn, disabled: S.configured ? null : "disabled" })));
    }
  }

  function logText(l) {
    const args = Object.assign({}, l.args || {});
    if (args.reason) args.reason = t("reason_" + args.reason);
    if (args.result) args.result = t("result_" + args.result);
    if (args.mode) args.mode = t("trigger_" + args.mode);
    return t("log_" + l.key, args);
  }
  function renderLogs() {
    const box = $("#logList");
    if (!box) return;
    box.innerHTML = "";
    const items = logs.slice(-300).reverse();
    if (!items.length) { box.append(el("div", { class: "empty", text: t("no_activity") })); return; }
    const frag = document.createDocumentFragment();
    for (const l of items) {
      frag.append(el("div", { class: "log " + l.level }, el("time", { text: fmtDate(l.time) }), el("span", { class: "msg", text: logText(l) })));
    }
    box.append(frag);
  }

  // ---------- folder picker ----------
  let pickPath = "";
  async function openPicker() {
    $("#pickModal").classList.remove("hidden");
    await browse("");
  }
  async function browse(p) {
    let r;
    try { r = await api("/api/browse?path=" + encodeURIComponent(p)); }
    catch (e) { toast(t("err_cannot_open")); return; }
    pickPath = r.path;
    const crumb = $("#pickCrumb");
    crumb.innerHTML = "";
    crumb.append(el("button", { text: "🖥 " + t("this_pc"), onclick: () => browse("") }));
    if (r.path) {
      const m = /^([A-Za-z]:\\|\\\\[^\\]+\\[^\\]+\\?|\/)/.exec(r.path);
      let acc = m ? m[0] : "";
      if (acc) crumb.append(" › ", el("button", { text: acc, onclick: ((x) => () => browse(x))(acc) }));
      const rest = r.path.slice(acc.length).split(/[\\/]/).filter(Boolean);
      const sep = r.path.includes("\\") ? "\\" : "/";
      for (const part of rest) {
        acc = acc + (acc.endsWith(sep) ? "" : sep) + part;
        crumb.append(" › ", el("button", { text: part, onclick: ((x) => () => browse(x))(acc) }));
      }
    }
    const list = $("#pickList");
    list.innerHTML = "";
    if (r.parent !== null && r.parent !== undefined) list.append(el("div", { class: "pick", onclick: () => browse(r.parent) }, el("span", { class: "pi", text: "↰" }), el("span", { class: "pn", text: t("up_one_level") })));
    const icons = { fixed: "💽", removable: "💾", network: "🌐", cdrom: "💿", shortcut: "⭐", other: "💽" };
    for (const e of r.entries || []) {
      list.append(el("div", { class: "pick", ondblclick: () => browse(e.path), onclick: () => browse(e.path) },
        el("span", { class: "pi", text: e.kind ? icons[e.kind] || "📁" : "📁" }),
        el("span", { class: "pn", text: e.name }),
        e.kind && e.kind !== "shortcut" ? el("span", { class: "pk", text: t("kind_" + e.kind) }) : null));
    }
    if (r.path && !(r.entries || []).length) list.append(el("div", { class: "empty", text: t("no_subfolders") }));
    $("#pickSel").textContent = r.path || t("pick_hint");
    $("#pickOk").disabled = !r.path;
  }
  async function pickConfirm() {
    if (!pickPath) return;
    const norm = (x) => x.replace(/[\\/]+$/, "").toLowerCase();
    if (S.settings.sources.some((s) => norm(s.path) === norm(pickPath))) { toast(t("already_added")); return; }
    const inside = S.settings.sources.find((s) => norm(pickPath).startsWith(norm(s.path) + (s.path.includes("/") ? "/" : "\\")));
    if (inside) { toast(t("inside_other", { path: inside.path })); return; }
    if (/^[A-Za-z]:\\?$/.test(pickPath)) {
      if (!(await confirmBox(t("whole_drive_title"), t("whole_drive_d")))) return;
    }
    S.settings.sources.push({ id: "", path: pickPath, enabled: true });
    $("#pickModal").classList.add("hidden");
    await saveNow();
    renderSources();
    renderHome();
  }

  // ---------- preview ----------
  async function showPreview() {
    const pv = await api("/api/preview").catch(() => null);
    if (!pv) { toast(t("preview_stopped")); return; }
    $("#prevSum").innerHTML = "";
    $("#prevSum").append(
      el("span", null, el("b", { text: num(pv.new) }), " " + t("pv_new")),
      el("span", null, el("b", { text: num(pv.update) }), " " + t("pv_update")),
      el("span", null, el("b", { text: num(pv.move || 0) }), " " + t("pv_move")),
      el("span", null, el("b", { text: num(pv.remove) }), " " + t("pv_remove")),
      el("span", null, el("b", { text: bytes(pv.bytes) }), " " + t("pv_upload")));
    const list = $("#prevList");
    list.innerHTML = "";
    if (!pv.items.length) list.append(el("div", { class: "empty", text: t("pv_nothing") }));
    const cls = { new: "new", update: "update", move: "update" };
    for (const it of pv.items) {
      const label = it.op === "move" ? it.from + "  →  " + it.path : it.path;
      const size = it.op === "move" ? "" : (it.size ? bytes(it.size) : "");
      list.append(el("div", { class: "prev-item" }, el("span", { class: "badge " + (cls[it.op] || "remove"), text: t("op_" + it.op) }), el("span", { class: "p", text: label }), el("span", { class: "muted", text: size })));
    }
    if (pv.truncated) list.append(el("div", { class: "empty", text: t("pv_more") }));
    for (const m of pv.missing || []) list.prepend(el("div", { class: "prev-item" }, el("span", { class: "badge remove", text: "!" }), el("span", { class: "p", text: t("log_source_missing", { path: m }) })));
    $("#prevGo").disabled = !pv.items.length;
    $("#prevModal").classList.remove("hidden");
  }

  // ---------- actions ----------
  let previewWaiting = false;
  async function runPreview() {
    try { await api("/api/sync", { dryRun: true }); }
    catch (e) { toast(t(e.message)); return; }
    previewWaiting = true;
    for (let i = 0; i < 1200 && previewWaiting; i++) {
      await new Promise((r) => setTimeout(r, 500));
      const r = await api("/api/status?since=" + logSeq).catch(() => null);
      if (r && !r.status.running) break;
    }
    if (!previewWaiting) return;
    previewWaiting = false;
    await refreshState();
    showPreview();
  }
  async function startSync(opts) {
    try { await api("/api/sync", opts || {}); await poll(); }
    catch (e) { toast(t(e.message)); }
  }
  async function signIn() {
    try { await api("/api/signin", {}); toast(t("signin_browser")); }
    catch (e) { toast(t(e.message)); }
  }
  async function signOut() {
    if (!(await confirmBox(t("btn_sign_out"), t("sign_out_d")))) return;
    await api("/api/signout", {});
    await refreshState();
  }
  async function refreshNetwork() {
    $("#netNow").textContent = t("checking");
    try {
      const n = await api("/api/network");
      ssidNow = n.ssid || "";
      const parts = [ssidNow ? t("net_wifi", { name: ssidNow }) : (n.ssidKnown === false ? t("net_wifi_hidden") : t("net_wired"))];
      parts.push(n.metered ? t("net_metered") : t("net_unmetered"));
      $("#netNow").textContent = parts.join(" · ");
    } catch (_) { $("#netNow").textContent = ""; }
  }

  // ---------- polling ----------
  let wasRunning = false;
  async function poll() {
    try {
      const r = await api("/api/status?since=" + logSeq);
      S.status = r.status;
      S.lastRun = r.lastRun;
      S.monthUsage = r.monthUsage;
      if (r.signedIn !== S.signedIn) { await refreshState(); return; }
      if (r.logs && r.logs.length) {
        logs = logs.concat(r.logs).slice(-500);
        logSeq = r.logs[r.logs.length - 1].seq;
        if (page === "activity") renderLogs();
      }
      if (wasRunning && !r.status.running) {
        await refreshState();
        if (previewWaiting) { previewWaiting = false; showPreview(); }
      }
      wasRunning = r.status.running;
      renderHome();
      if (page === "internet") renderDerived();
    } catch (_) { }
  }
  async function refreshState() {
    const fresh = await api("/api/state");
    if (dirty && S) fresh.settings = S.settings; // keep edits that are not saved yet
    S = Object.assign(S || {}, fresh);
    renderAll();
  }

  // ---------- wire up ----------
  function wire() {
    $$("#nav button").forEach((b) => b.addEventListener("click", () => show(b.dataset.page)));
    $("#langPick").addEventListener("change", (e) => changeLang(e.target.value));
    $("#langPick2").addEventListener("change", (e) => changeLang(e.target.value));
    $("#btnSync").addEventListener("click", () => startSync({}));
    $("#btnForce").addEventListener("click", () => startSync({ force: true }));
    $("#btnPreview").addEventListener("click", runPreview);
    $("#btnStop").addEventListener("click", () => api("/api/stop", {}));
    $("#prevGo").addEventListener("click", () => { $("#prevModal").classList.add("hidden"); startSync({}); });
    $("#btnFullCheck").addEventListener("click", () => { startSync({ fullCheck: true }); show("home"); });
    $("#btnAddFolder").addEventListener("click", openPicker);
    $("#pickOk").addEventListener("click", pickConfirm);
    $$("[data-close]").forEach((b) => b.addEventListener("click", () => b.closest(".modal").classList.add("hidden")));
    document.addEventListener("keydown", (e) => { if (e.key === "Escape") $$(".modal").forEach((m) => { if (m.id !== "confirmModal") m.classList.add("hidden"); }); });
    $("#excludes").addEventListener("input", () => { S.settings.excludes = $("#excludes").value.split(/\r?\n/).map((s) => s.trim()).filter(Boolean); scheduleSave(); });
    $("#btnResetEx").addEventListener("click", async () => { S.settings.excludes = await api("/api/reset-excludes", {}); $("#excludes").value = S.settings.excludes.join("\n"); scheduleSave(); });
    $("#capGB").addEventListener("input", () => { const g = parseFloat($("#capGB").value) || 0; S.settings.monthlyCapMB = Math.max(0, Math.round(g * 1024)); scheduleSave(); renderDerived(); });
    $("#blockedAdd").addEventListener("click", () => addChip("blockedWifi", "#blockedIn"));
    $("#allowedAdd").addEventListener("click", () => addChip("allowedWifi", "#allowedIn"));
    $("#blockedIn").addEventListener("keydown", (e) => { if (e.key === "Enter") addChip("blockedWifi", "#blockedIn"); });
    $("#allowedIn").addEventListener("keydown", (e) => { if (e.key === "Enter") addChip("allowedWifi", "#allowedIn"); });
    $("#blockedCur").addEventListener("click", () => ssidNow ? addChip("blockedWifi", null, ssidNow) : toast(t("no_wifi_now")));
    $("#allowedCur").addEventListener("click", () => ssidNow ? addChip("allowedWifi", null, ssidNow) : toast(t("no_wifi_now")));
    $("#btnLogs").addEventListener("click", () => api("/api/open", { what: "logs" }));
    $("#lnkLangFolder").addEventListener("click", (e) => { e.preventDefault(); api("/api/open", { what: "languages" }); });
    $("#btnAuthor").addEventListener("click", () => api("/api/open", { what: "author" }));
    $("#btnGcloud").addEventListener("click", () => api("/api/open", { what: "gcloud" }));
    $("#btnSaveCreds").addEventListener("click", async () => {
      const id = $("#clientId").value.trim(), sec = $("#clientSecret").value.trim();
      if (id !== (S.settings.clientId || "") && S.signedIn && !(await confirmBox(t("advanced_title"), t("creds_change_d")))) return;
      S.settings.clientId = id; S.settings.clientSecret = sec;
      dirty = true; editVer++;
      await saveNow();
      credsDirty = false;
      await refreshState();
    });
    $("#btnQuit").addEventListener("click", async () => { if (await confirmBox(t("btn_quit"), t("quit_d"))) { await api("/api/quit", {}); window.close(); } });
    $("#btnUninstall").addEventListener("click", async () => {
      if (!(await confirmBox(t("uninstall_title"), t("uninstall_confirm")))) return;
      await api("/api/uninstall", { removeData: $("#unData").checked });
      document.body.innerHTML = "";
      document.body.append(el("div", { class: "card", style: "margin:40px", text: t("uninstall_done") }));
    });
    $("#clientId").addEventListener("input", () => { credsDirty = true; });
    $("#clientSecret").addEventListener("input", () => { credsDirty = true; });
    bindInputs();
    // A newer window replaces older ones so only one stays open.
    try {
      const bc = new BroadcastChannel("selva-sync");
      bc.onmessage = (e) => { if (e.data === "hello") window.close(); };
      bc.postMessage("hello");
    } catch (_) { }
  }

  async function init() {
    wire();
    S = await api("/api/state");
    await loadLang(S.settings.language);
    fillLangPickers();
    const r = await api("/api/status?since=0");
    logs = r.logs || [];
    if (logs.length) logSeq = logs[logs.length - 1].seq;
    renderAll();
    show("home");
    setInterval(poll, 1500);
    setInterval(() => refreshState().catch(() => { }), 20000);
    window.addEventListener("focus", () => refreshState().catch(() => { }));
  }
  init().catch((e) => { document.body.textContent = "Selva Sync: " + e.message; });
})();
