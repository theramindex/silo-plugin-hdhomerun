"use strict";
(() => {
  const $ = (id) => document.getElementById(id);
  const root = location.pathname.split("/hdhomerun")[0];
  const base = root + "/hdhomerun";
  const installation = (root.match(/\/plugins\/(\d+)$/) || [])[1] || "";
  const adminPage = location.pathname.endsWith("/admin");
  const state = {
    channels: [],
    endpoints: [],
    programs: [],
    config: { endpoints: [] },
    favorites: {},
    source: "",
    search: "",
    onlyFavorites: false,
    selected: null,
    offset: 0,
    relay: false,
    editing: -1,
    heartbeat: null,
    session: "",
    player: null,
    hls: null,
    generation: 0,
    write: Promise.resolve(),
    loading: false,
  };
  let token = "",
    refreshing;
  const read = (key) => {
    try {
      return localStorage.getItem(key) || "";
    } catch (_) {
      return "";
    }
  };
  const profile = () => read("profile_id");
  function headers() {
    const h = {};
    if (token) h.Authorization = "Bearer " + token;
    if (profile()) h["X-Profile-Id"] = profile();
    if (read("profile_token")) h["X-Profile-Token"] = read("profile_token");
    return h;
  }
  async function refreshSession() {
    if (!read("refresh_token")) return false;
    const r = await fetch("/api/v2/auth/refresh", {
      method: "POST",
      credentials: "include",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ refresh_token: read("refresh_token") }),
    });
    if (!r.ok) return false;
    const b = await r.json();
    token = b.access_token || "";
    if (b.refresh_token) {
      try {
        localStorage.setItem("refresh_token", b.refresh_token);
      } catch (_) {}
    }
    return !!token;
  }
  async function fetchAuth(path, options = {}) {
    if (!token && read("refresh_token")) {
      refreshing ||= refreshSession().finally(() => {
        refreshing = null;
      });
      await refreshing;
    }
    const send = () =>
      fetch(path, {
        ...options,
        credentials: "include",
        headers: { ...headers(), ...options.headers },
      });
    let r = await send();
    if (r.status === 401) {
      refreshing ||= refreshSession().finally(() => {
        refreshing = null;
      });
      if (await refreshing) r = await send();
    }
    return r;
  }
  async function api(path, method = "GET", body, core = false) {
    const r = await fetchAuth((core ? "/api/v2" : base) + path, {
      method,
      ...(body !== undefined
        ? {
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify(body),
          }
        : {}),
    });
    if (!core) state.relay = r.headers.get("X-Silo-Stream-Relay") === "1";
    if (!r.ok) {
      const b = await r.json().catch(() => ({}));
      throw new Error(
        b.error ||
          {
            401: "Sign in to Silo to continue.",
            403: "Administrator access required.",
            409: "All available tuners are in use.",
          }[r.status] ||
          "The request could not finish. Try again.",
      );
    }
    return r.status === 204 ? {} : r.json();
  }
  const esc = (text) =>
    String(text ?? "").replace(
      /[&<>"']/g,
      (c) =>
        ({
          "&": "&amp;",
          "<": "&lt;",
          ">": "&gt;",
          '"': "&quot;",
          "'": "&#39;",
        })[c],
    );
  const clock = (unix) =>
    new Date(unix * 1000).toLocaleTimeString([], {
      hour: "numeric",
      minute: "2-digit",
    });
  function notice(message) {
    $("notice").textContent = message;
    $("notice").hidden = !message;
  }
  function nowProgramme(c) {
    const now = Date.now() / 1000;
    return state.programs.find(
      (p) => p.channelId === c.id && p.startUnix <= now && p.endUnix > now,
    );
  }
  function visible() {
    return state.channels.filter(
      (c) =>
        (!state.source ||
          c.sources.some((s) => s.endpointId === state.source)) &&
        (!state.onlyFavorites || state.favorites[c.id]) &&
        (!state.search ||
          (c.name + " " + c.number).toLowerCase().includes(state.search)),
    );
  }
  function selectedGuide() {
    return location.hash === "#guide";
  }
  function renderChannels() {
    const list = visible();
    const guide = selectedGuide();
    $("guide-tools").hidden = !guide;
    $("channels-tab").setAttribute("aria-current", guide ? "false" : "page");
    $("guide-tab").setAttribute("aria-current", guide ? "page" : "false");
    $("channel-count").textContent = list.length + " channels";
    $("empty").hidden = list.length > 0;
    $("empty").textContent = !state.channels.length
      ? state.endpoints.length
        ? "No channels yet. An administrator can check endpoint health and refresh the lineup in Devices."
        : "No endpoints connected yet. An administrator can add your tuner or emulator in Devices."
      : "No channels match these filters.";
    $("guide-time").textContent = new Date(
      (Math.floor(Date.now() / 1000 / 10800) * 10800 + state.offset) * 1000,
    ).toLocaleString([], {
      weekday: "short",
      hour: "numeric",
      minute: "2-digit",
    });
    const start = Math.floor(Date.now() / 1000 / 10800) * 10800 + state.offset;
    const end = start + 10800;
    $("channel-list").innerHTML = list
      .map((c) => {
        const p = nowProgramme(c);
        const star = `<button class="star" data-favorite="${esc(c.id)}" aria-label="${state.favorites[c.id] ? "Remove" : "Add"} ${esc(c.name)} ${state.favorites[c.id] ? "from" : "to"} favorites" aria-pressed="${!!state.favorites[c.id]}">${state.favorites[c.id] ? "★" : "☆"}</button>`;
        if (guide) {
          const programs = state.programs.filter(
            (p) =>
              p.channelId === c.id && p.startUnix < end && p.endUnix > start,
          );
          return `<article class="guide-row"><div class="guide-title"><span class="number">${esc(c.number)}</span><button data-channel="${esc(c.id)}"><strong>${esc(c.name)}</strong></button>${star}</div><div class="programme-rail">${programs.length ? programs.map((p) => `<button class="programme ${p.startUnix <= Date.now() / 1000 && p.endUnix > Date.now() / 1000 ? "live" : ""}" data-program="${esc(p.id)}"><time>${clock(p.startUnix)}–${clock(p.endUnix)}</time><strong>${esc(p.title)}</strong>${p.isLive ? '<span class="badge">LIVE</span>' : ""}</button>`).join("") : '<span class="muted">No programme data in this window.</span>'}</div></article>`;
        }
        const pct = p
          ? Math.max(
              0,
              Math.min(
                100,
                ((Date.now() / 1000 - p.startUnix) /
                  (p.endUnix - p.startUnix)) *
                  100,
              ),
            )
          : 0;
        return `<article class="channel-row"><span class="number">${esc(c.number)}</span><button class="channel-main" data-channel="${esc(c.id)}"><strong>${esc(c.name)}${c.protected ? " · Protected" : ""}</strong><span>${p ? esc(p.title) : "No programme information"}</span>${p ? `<div class="progress" aria-hidden="true"><i style="width:${pct}%"></i></div>` : ""}</button>${star}</article>`;
      })
      .join("");
  }
  function selectChannel(c, p) {
    state.selected = c;
    if (!p) p = nowProgramme(c);
    const active = state.session && $("video");
    if (!active)
      $("detail").innerHTML =
        '<video id="video" controls playsinline hidden></video><div id="play-status" role="status" hidden></div><div class="detail-body" id="detail-body"></div>';
    $("detail-body").innerHTML =
      `<span class="eyebrow">${esc(c.number)} · ${esc(c.name)}</span><h2>${esc(p?.title || c.name)}</h2>${p ? `<span class="muted">${clock(p.startUnix)}–${clock(p.endUnix)}</span><p>${esc(p.summary || "")}</p>` : "<p>Programme information is unavailable. You can still watch this channel.</p>"}<p>${esc(c.sources.map((s) => s.name).join(" · "))}</p><div class="actions"><button class="primary" id="watch" ${c.protected ? "disabled" : ""}>${c.protected ? "Protected channel" : "▶ Watch live"}</button><button id="stop" ${!state.session ? "hidden" : ""}>Stop</button></div>`;
    $("watch").onclick = () => play(c);
    $("stop").onclick = () => stopPlayer();
  }
  async function loadScript(name) {
    if (document.querySelector(`script[data-library="${name}"]`)) return;
    const r = await fetchAuth(base + "/assets/" + name);
    if (!r.ok) throw new Error("Unable to load the video player.");
    const blob = new Blob([await r.text()], { type: "text/javascript" });
    const u = URL.createObjectURL(blob);
    try {
      await new Promise((resolve, reject) => {
        const script = document.createElement("script");
        script.dataset.library = name;
        script.src = u;
        script.onload = resolve;
        script.onerror = () => {
          script.remove();
          reject(new Error("Unable to load the video player."));
        };
        document.head.append(script);
      });
    } finally {
      URL.revokeObjectURL(u);
    }
  }
  function playerStatus(message) {
    if ($("play-status")) {
      $("play-status").textContent = message;
      $("play-status").hidden = !message;
    }
  }
  async function stopPlayer() {
    state.generation++;
    const session = state.session;
    state.session = "";
    clearInterval(state.heartbeat);
    state.heartbeat = null;
    if (state.hls) {
      state.hls.destroy();
      state.hls = null;
    }
    if (state.player) {
      state.player.destroy();
      state.player = null;
    }
    const v = $("video");
    if (v) {
      v.pause();
      v.removeAttribute("src");
      v.load();
      v.hidden = true;
    }
    if ($("stop")) $("stop").hidden = true;
    playerStatus("");
    if (session)
      try {
        await api(
          "/api/session/stop?session_id=" + encodeURIComponent(session),
          "POST",
        );
      } catch (e) {
        notice(
          "Playback stopped locally. The server reservation will expire automatically.",
        );
      }
  }
  async function play(c) {
    const stopping = stopPlayer();
    const generation = state.generation;
    await stopping;
    if (generation !== state.generation) return;
    selectChannel(c);
    const session = crypto.randomUUID();
    let allocated = false;
    try {
      if (!state.relay)
        throw new Error(
          "Browser playback needs the stream relay update in the Ramindex Silo server. Channels remain available to compatible TV clients.",
        );
      playerStatus("Finding an available tuner…");
      const result = await api("/api/play", "POST", {
        channelId: c.id,
        sessionId: session,
        format: "any",
      });
      allocated = true;
      if (generation !== state.generation) {
        await api("/api/session/stop?session_id=" + session, "POST");
        return;
      }
      state.session = session;
      const v = $("video");
      v.onplaying = () => playerStatus("");
      v.onerror = () =>
        playerStatus(
          "The browser could not decode this stream. Try a compatible TV client.",
        );
      v.onended = () => stopPlayer();
      v.hidden = false;
      $("stop").hidden = false;
      const media = root + result.path;
      if (result.container === "hls") {
        await loadScript("hls.min.js");
        if (generation !== state.generation) return;
        if (window.Hls?.isSupported()) {
          const hls = new Hls({
            xhrSetup: (xhr, url) => {
              if (new URL(url, location.href).origin !== location.origin)
                throw new Error("Unapproved media origin");
              Object.entries(headers()).forEach(([k, v]) =>
                xhr.setRequestHeader(k, v),
              );
              xhr.withCredentials = true;
            },
          });
          state.hls = hls;
          hls.loadSource(media);
          hls.attachMedia(v);
          hls.on(Hls.Events.MANIFEST_PARSED, () =>
            v.play().catch(() => playerStatus("Press Play to start.")),
          );
          hls.on(Hls.Events.ERROR, (_, d) => {
            if (d.fatal)
              playerStatus(
                "This stream could not play. Stop and try again, or use a compatible TV client.",
              );
          });
        } else if (v.canPlayType("application/vnd.apple.mpegurl")) {
          v.src = media;
          await v.play();
        } else throw new Error("This browser does not support HLS playback.");
      } else {
        await loadScript("mpegts.min.js");
        if (generation !== state.generation) return;
        if (!window.mpegts?.isSupported())
          throw new Error(
            "This browser cannot play MPEG-TS directly. Use an HLS emulator or a compatible TV client.",
          );
        const player = mpegts.createPlayer(
          { type: "mpegts", isLive: true, url: media },
          {
            enableWorker: false,
            enableStashBuffer: false,
            lazyLoad: false,
            headers: headers(),
          },
        );
        state.player = player;
        player.attachMediaElement(v);
        player.load();
        player.on(mpegts.Events.ERROR, () =>
          playerStatus(
            "The stream could not play. OTA MPEG-2 / AC-3 may need conversion at the source or a compatible TV client.",
          ),
        );
        await player.play();
      }
      if (generation !== state.generation) return;
      if (!v.paused && v.readyState >= 2) playerStatus("");
      state.heartbeat = setInterval(async () => {
        try {
          await api("/api/session/heartbeat?session_id=" + session, "POST");
        } catch (e) {
          playerStatus("The tuner session expired. Stop and try again.");
        }
      }, 25000);
    } catch (e) {
      if (generation === state.generation) {
        if (allocated) await stopPlayer();
        playerStatus(e.message);
      }
    }
  }
  async function preferences() {
    if (!installation) return;
    const payload = await api(
      "/settings/plugins/" + installation,
      "GET",
      undefined,
      true,
    );
    const raw =
      typeof payload.values?.preferences === "string"
        ? JSON.parse(payload.values.preferences || "{}")
        : payload.values?.preferences || {};
    const view = raw.profiles?.[profile()] || raw;
    state.favorites = view.favorites || {};
  }
  function favorite(id) {
    const value = !state.favorites[id];
    const viewer = profile();
    state.write = state.write
      .catch(() => {})
      .then(async () => {
        try {
          const path = "/settings/plugins/" + installation;
          const payload = await api(path, "GET", undefined, true);
          if (viewer !== profile())
            throw new Error(
              "Profile changed. Reload before editing favorites.",
            );
          const values = payload.values || {};
          const raw =
            typeof values.preferences === "string"
              ? JSON.parse(values.preferences || "{}")
              : values.preferences || {};
          let target = raw;
          if (viewer) {
            raw.profiles ||= {};
            if (!raw.profiles[viewer])
              raw.profiles[viewer] = {
                favorites: { ...(raw.favorites || {}) },
                favoriteOrder: [...(raw.favoriteOrder || [])],
                hiddenCategories: { ...(raw.hiddenCategories || {}) },
              };
            target = raw.profiles[viewer];
          }
          target.favorites = { ...(target.favorites || {}) };
          if (value) target.favorites[id] = true;
          else delete target.favorites[id];
          target.favoriteOrder = (target.favoriteOrder || []).filter(
            (x) => x !== id,
          );
          if (value) target.favoriteOrder.push(id);
          values.preferences = JSON.stringify(raw);
          await api(path, "PUT", { values }, true);
          state.favorites = target.favorites;
          renderChannels();
        } catch (e) {
          notice("Could not save favorites: " + e.message);
        }
      });
  }
  async function loadGuide() {
    const start = Math.floor(Date.now() / 1000 / 10800) * 10800 + state.offset;
    const ids = visible().map((c) => c.id);
    const out = [];
    for (let i = 0; i < ids.length; i += 100) {
      const result = await api(
        "/api/guide?start=" +
          start +
          "&end=" +
          (start + 10800) +
          "&channel_ids=" +
          encodeURIComponent(ids.slice(i, i + 100).join(",")),
      );
      out.push(...result.programs);
    }
    state.programs = out;
    renderChannels();
  }
  async function loadState() {
    const data = await api("/api/state");
    const options =
      '<option value="">All endpoints</option>' +
      (data.endpoints || [])
        .map((e) => `<option value="${esc(e.id)}">${esc(e.name)}</option>`)
        .join("");
    if ($("source").innerHTML !== options) {
      $("source").innerHTML = options;
      $("source").value = state.source;
      if (!$("source").value) state.source = "";
    }
    Object.assign(state, {
      channels: data.channels || [],
      endpoints: data.endpoints || [],
    });
    $("devices-tab").hidden = !data.admin;
    $("devices-tab").href = base + "/admin";
    $("connection").textContent = data.refreshing
      ? "Refreshing…"
      : state.endpoints.filter((e) => e.enabled && !e.error).length +
        " endpoints online";
    if (adminPage) renderDevices();
    else {
      renderChannels();
      if (!state.loading) {
        state.loading = true;
        try {
          await loadGuide();
        } finally {
          state.loading = false;
        }
      }
    }
  }
  function renderDevices() {
    $("device-list").innerHTML = state.config.endpoints.length
      ? state.config.endpoints
          .map((e, i) => {
            const health = state.endpoints.find((s) => s.id === e.id);
            return `<article class="device"><div><h3>${esc(e.name || e.id)}</h3><p>${esc(e.url)}</p><p>${esc(health?.model || "HDHomeRun-compatible endpoint")}</p></div><div class="device-health"><p class="${health?.error ? "error" : "health"}">${e.enabled === false ? "Disabled" : health?.error ? esc(health.error) : health?.lastSuccessUnix ? "Connected" : "Waiting for discovery"}</p><p>${health?.channelCount || 0} channels · ${health?.inUse || 0} / ${health?.tunerCount || "—"} tuners reserved</p>${health?.guideError ? `<p class="error">Guide: ${esc(health.guideError)}</p>` : e.guide_mode && e.guide_mode !== "none" ? `<p>${health?.programCount || 0} programmes</p>` : "<p>No guide configured</p>"}</div><div class="actions"><button data-edit="${i}">Edit</button><button data-remove="${i}">Remove</button></div></article>`;
          })
          .join("")
      : '<p class="device-empty">No endpoints connected. Add the HTTP address of your tuner or emulator to discover its lineup. Each endpoint keeps its own channels and tuner capacity.</p>';
  }
  function edit(index) {
    state.editing = index;
    const e = state.config.endpoints[index] || {
      name: "",
      url: "",
      enabled: true,
      tuner_limit: 0,
      priority: 0,
      guide_mode: "none",
      allowed_hosts: [],
      channels: {},
    };
    const form = $("editor");
    form.reset();
    for (const name of [
      "name",
      "url",
      "tuner_limit",
      "priority",
      "guide_mode",
      "guide_url",
      "guide_url_file",
    ])
      form.elements[name].value =
        e[name] ?? (name === "tuner_limit" || name === "priority" ? 0 : "");
    form.elements.enabled.checked = e.enabled !== false;
    form.elements.allowed_hosts.value = (e.allowed_hosts || []).join(", ");
    $("edit-title").textContent =
      index < 0 ? "Add endpoint" : "Edit " + (e.name || "endpoint");
    const numbers = new Map();
    state.channels.forEach((c) =>
      c.sources
        .filter((s) => s.endpointId === e.id)
        .forEach((s) => numbers.set(s.number, c.name)),
    );
    Object.keys(e.channels || {}).forEach((n) => {
      if (!numbers.has(n)) numbers.set(n, n);
    });
    $("mapping-rows").innerHTML = Array.from(numbers)
      .map(([n, name]) => {
        const m = e.channels?.[n] || {};
        return `<tr data-number="${esc(n)}"><td>${esc(n)} · ${esc(name)}</td><td><input aria-label="Display name for ${esc(n)}" data-field="name" value="${esc(m.name || "")}"></td><td><input aria-label="Station key for ${esc(n)}" data-field="station_id" value="${esc(m.station_id || "")}" pattern="[a-zA-Z0-9_-]{1,64}"></td><td><input aria-label="Guide ID for ${esc(n)}" data-field="guide_id" value="${esc(m.guide_id || "")}"></td><td><input type="checkbox" aria-label="Hide ${esc(n)}" data-field="hidden" ${m.hidden ? "checked" : ""}></td></tr>`;
      })
      .join("");
    $("mapping-empty").hidden = numbers.size > 0;
    $("save-status").textContent = "";
    form.hidden = false;
    form.scrollIntoView({ block: "start", behavior: "instant" });
    form.elements.name.focus();
  }
  async function persist(cfg) {
    const normalized = await api("/admin/api/validate", "POST", cfg);
    await api(
      "/admin/plugins/installations/" + installation + "/config",
      "PUT",
      { key: "connection", value: normalized },
      true,
    );
    state.config = normalized;
    await api("/admin/api/refresh", "POST");
    await loadState();
  }
  $("editor").onsubmit = async (event) => {
    event.preventDefault();
    $("save").disabled = true;
    try {
      const form = event.currentTarget;
      const existing = state.config.endpoints[state.editing] || {};
      const e = {
        ...existing,
        id: existing.id || "device-" + crypto.randomUUID(),
        name: form.elements.name.value.trim(),
        url: form.elements.url.value.trim(),
        enabled: form.elements.enabled.checked,
        tuner_limit: Number(form.elements.tuner_limit.value),
        priority: Number(form.elements.priority.value),
        guide_mode: form.elements.guide_mode.value,
        guide_url: form.elements.guide_url.value.trim(),
        guide_url_file: form.elements.guide_url_file.value.trim(),
        allowed_hosts: form.elements.allowed_hosts.value
          .split(",")
          .map((s) => s.trim())
          .filter(Boolean),
        channels: { ...(existing.channels || {}) },
      };
      $("mapping-rows")
        .querySelectorAll("tr")
        .forEach((row) => {
          const m = {};
          row.querySelectorAll("input").forEach((input) => {
            const value =
              input.type === "checkbox" ? input.checked : input.value.trim();
            if (value) m[input.dataset.field] = value;
          });
          e.channels[row.dataset.number] = m;
        });
      const cfg = structuredClone(state.config);
      if (state.editing < 0) cfg.endpoints.push(e);
      else cfg.endpoints[state.editing] = e;
      await persist(cfg);
      $("editor").hidden = true;
      notice("Endpoint saved. The lineup is refreshing.");
    } catch (e) {
      $("save-status").textContent = e.message;
    } finally {
      $("save").disabled = false;
    }
  };
  $("device-list").onclick = async (event) => {
    const editButton = event.target.closest("[data-edit]");
    if (editButton) return edit(Number(editButton.dataset.edit));
    const remove = event.target.closest("[data-remove]");
    if (!remove) return;
    const i = Number(remove.dataset.remove);
    if (
      !confirm(
        "Remove " +
          state.config.endpoints[i].name +
          "? Active streams will stop.",
      )
    )
      return;
    remove.disabled = true;
    try {
      const cfg = structuredClone(state.config);
      cfg.endpoints.splice(i, 1);
      await persist(cfg);
      $("editor").hidden = true;
    } catch (e) {
      notice(e.message);
    } finally {
      remove.disabled = false;
    }
  };
  $("add").onclick = () => edit(-1);
  $("cancel").onclick = () => {
    $("editor").hidden = true;
  };
  $("refresh").onclick = async () => {
    try {
      await api("/admin/api/refresh", "POST");
      notice("Refreshing endpoints and guides that are due.");
    } catch (e) {
      notice(e.message);
    }
  };
  $("channel-list").onclick = (event) => {
    const favoriteButton = event.target.closest("[data-favorite]");
    if (favoriteButton) return favorite(favoriteButton.dataset.favorite);
    const b = event.target.closest("[data-channel]");
    if (b) {
      const c = state.channels.find((c) => c.id === b.dataset.channel);
      if (c) selectChannel(c);
      return;
    }
    const p = event.target.closest("[data-program]");
    if (p) {
      const program = state.programs.find((x) => x.id === p.dataset.program);
      const c = state.channels.find((c) => c.id === program?.channelId);
      if (c) selectChannel(c, program);
    }
  };
  $("search").oninput = (event) => {
    state.search = event.target.value.toLowerCase();
    renderChannels();
  };
  $("source").onchange = async (event) => {
    state.source = event.target.value;
    renderChannels();
    try {
      await loadGuide();
    } catch (e) {
      notice(e.message);
    }
  };
  $("favorites").onclick = () => {
    state.onlyFavorites = !state.onlyFavorites;
    $("favorites").setAttribute("aria-pressed", String(state.onlyFavorites));
    renderChannels();
    loadGuide().catch((e) => notice(e.message));
  };
  for (const [id, offset] of [
    ["previous", -10800],
    ["next", 10800],
    ["now", 0],
  ])
    $(id).onclick = () => {
      state.offset =
        id === "now"
          ? 0
          : Math.max(-86400, Math.min(14 * 86400, state.offset + offset));
      loadGuide().catch((e) => notice(e.message));
    };
  window.onhashchange = () => {
    renderChannels();
    loadGuide().catch((e) => notice(e.message));
  };
  window.addEventListener("pagehide", () => stopPlayer());
  window.addEventListener("storage", (event) => {
    if (event.key === "profile_id") {
      stopPlayer()
        .then(() => preferences())
        .then(() => renderChannels())
        .catch((e) => notice(e.message));
    }
  });
  async function start() {
    $("admin").hidden = !adminPage;
    $("viewer").hidden = adminPage;
    if (adminPage) {
      $("channels-tab").href = base + "#channels";
      $("guide-tab").href = base + "#guide";
      $("devices-tab").setAttribute("aria-current", "page");
    }
    try {
      await loadState();
      $("source").innerHTML =
        '<option value="">All endpoints</option>' +
        state.endpoints
          .map((e) => `<option value="${esc(e.id)}">${esc(e.name)}</option>`)
          .join("");
      if (adminPage) {
        state.config = await api("/admin/api/config");
        if (!Array.isArray(state.config.endpoints)) state.config.endpoints = [];
        renderDevices();
      } else {
        try {
          await preferences();
          renderChannels();
        } catch (e) {
          notice("Favorites could not load. " + e.message);
        }
      }
      setInterval(() => {
        if (!document.hidden) loadState().catch((e) => notice(e.message));
      }, 15000);
    } catch (e) {
      notice(e.message);
      $("connection").textContent = "Unavailable";
    }
  }
  start();
})();
