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
 recent:[], media:null, tiles:new Map(),
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
    page: 0,
    guideRequest: "",
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
  const icons = {
    star: '<path d="m12 3 2.8 5.7 6.2.9-4.5 4.4 1.1 6.2-5.6-3-5.6 3 1.1-6.2L3 9.6l6.2-.9L12 3Z"/>',
    play: '<path d="m9 5 11 7-11 7V5Z"/>',
    close: '<path d="m6 6 12 12M6 18 18 6"/>',
    tv: '<rect x="3" y="5" width="18" height="15" rx="2"/><path d="m8 2 4 3 4-3"/>',
  };
  const icon = (name) =>
    '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">' +
    icons[name] +
    "</svg>";
  function pageChannels() {
    const list = visible();
    state.page = Math.min(
      state.page,
      Math.max(0, Math.ceil(list.length / 60) - 1),
    );
    return list.slice(state.page * 60, state.page * 60 + 60);
  }
  function logo(c) {
    let src = "";
    try {
      const u = new URL(c.logoUrl);
      if (
        ["http:", "https:"].includes(u.protocol) &&
        !u.username &&
        !u.password
      )
        src = u.href;
    } catch (_) {}
    return (
      '<span class="channel-logo">' +
      (src
        ? '<img src="' +
          esc(src) +
          '" alt="" loading="lazy" referrerpolicy="no-referrer">'
        : esc(c.number)) +
      "</span>"
    );
  }
  function star(c) {
    return `<button class="star" data-favorite="${esc(c.id)}" aria-label="${state.favorites[c.id] ? "Remove" : "Add"} ${esc(c.name)} ${state.favorites[c.id] ? "from" : "to"} favorites" aria-pressed="${!!state.favorites[c.id]}">${icon("star")}</button>`;
  }
  function renderGroups() {
    const counts = new Map();
    state.channels.forEach((c) =>
      c.sources.forEach((source) =>
        counts.set(source.endpointId, (counts.get(source.endpointId) || 0) + 1),
      ),
    );
    $("all-count").textContent = state.channels.length.toLocaleString();
    $("favorite-count").textContent = state.channels
      .filter((c) => state.favorites[c.id])
      .length.toLocaleString();
    $("all-channels").classList.toggle(
      "active",
      !state.source && !state.onlyFavorites,
    );
    $("favorites").setAttribute("aria-pressed", String(state.onlyFavorites));
    $("source-groups").innerHTML = state.endpoints
      .map(
        (e) =>
          `<button type="button" class="group-button ${state.source === e.id ? "active" : ""}" data-source="${esc(e.id)}" aria-pressed="${state.source === e.id}"><span>${esc(e.name)}</span><small>${(counts.get(e.id) || 0).toLocaleString()}</small></button>`,
      )
      .join("");
  }
  function renderChannels() {
 renderRecent();
    const list = visible();
    const page = pageChannels();
    const guide = selectedGuide();
    $("viewer").classList.toggle("is-guide", guide);
    $("guide-tools").hidden = !guide;
    $("channels-tab").setAttribute(
      "aria-current",
      !guide && !state.onlyFavorites ? "page" : "false",
    );
    $("guide-tab").setAttribute("aria-current", guide ? "page" : "false");
    $("favorites-tab").setAttribute(
      "aria-current",
      !guide && state.onlyFavorites ? "page" : "false",
    );
    $("channel-count").textContent = list.length.toLocaleString() + " channels";
    $("empty").hidden = list.length > 0;
    $("empty").textContent = !state.channels.length
      ? state.endpoints.length
        ? "No channels yet. Check the endpoint connection in Devices, then refresh the lineup."
        : "No devices connected yet. An administrator can add a tuner or emulator in Devices."
      : "No channels match these filters.";
    $("page-range").textContent = list.length
      ? `${(state.page * 60 + 1).toLocaleString()}–${(state.page * 60 + page.length).toLocaleString()} of ${list.length.toLocaleString()}`
      : "0 channels";
    $("page-previous").disabled = state.page === 0;
    $("page-next").disabled = (state.page + 1) * 60 >= list.length;
    const start = Math.floor(Date.now() / 1000 / 10800) * 10800 + state.offset;
    const end = start + 10800;
    const now = Date.now() / 1000;
    $("guide-time").textContent =
      new Date(start * 1000).toLocaleString([], {
        weekday: "short",
        month: "short",
        day: "numeric",
      }) +
      " · " +
      clock(start) +
      "–" +
      clock(end);
    let body = page
      .map((c) => {
        const p = nowProgramme(c);
        const selected = state.selected?.id === c.id;
        if (guide) {
          const programs = state.programs.filter(
            (p) =>
              p.channelId === c.id && p.startUnix < end && p.endUnix > start,
          );
          const cells = programs
            .map((p) => {
              const left =
                ((Math.max(p.startUnix, start) - start) / 10800) * 100;
              const width =
                ((Math.min(p.endUnix, end) - Math.max(p.startUnix, start)) /
                  10800) *
                100;
              return `<button class="programme ${p.startUnix <= now && p.endUnix > now ? "live" : ""}" data-program="${esc(p.id)}" style="left:${left}%;width:calc(${width}% - 4px)" title="${esc(p.title)}" aria-label="${esc(c.name + " · " + p.title + " · " + clock(p.startUnix) + "–" + clock(p.endUnix))}"><strong>${esc(p.title)}</strong><time>${clock(p.startUnix)}–${clock(p.endUnix)}</time></button>`;
            })
            .join("");
          const marker =
            now >= start && now < end
              ? `<span class="guide-now" style="left:${((now - start) / 10800) * 100}%" aria-hidden="true"></span>`
              : "";
          return `<article class="guide-row ${selected ? "selected" : ""}"><div class="guide-title"><button class="channel-main" data-channel="${esc(c.id)}" aria-label="${esc(c.number + " " + c.name)}">${logo(c)}<span class="channel-name"><strong>${esc(c.name)}</strong><small>${esc(c.number)}</small></span></button>${star(c)}</div><div class="programme-rail">${cells || '<span class="programme-empty">Programme information unavailable</span>'}${marker}</div></article>`;
        }
        const pct = p
          ? Math.max(
              0,
              Math.min(
                100,
                ((now - p.startUnix) / (p.endUnix - p.startUnix)) * 100,
              ),
            )
          : 0;
        return `<article class="channel-row ${selected ? "selected" : ""}"><button class="channel-main" data-channel="${esc(c.id)}" aria-label="${esc(c.number + " " + c.name)}">${logo(c)}<span class="channel-name"><strong>${esc(c.name)}</strong><small>${esc(c.number)}${c.protected ? " · Protected" : c.sources.length > 1 ? " · " + c.sources.length + " sources" : ""}</small></span><span class="channel-program"><strong>${p ? esc(p.title) : "Programme unavailable"}</strong>${p ? "<small>" + clock(p.startUnix) + "–" + clock(p.endUnix) + '</small><span class="progress" aria-hidden="true"><i style="width:' + pct + '%"></i></span>' : "<small>Watch live for this channel</small>"}</span></button>${star(c)}</article>`;
      })
      .join("");
    if (guide && page.length) {
      let times = "";
      for (let offset = 0; offset < 10800; offset += 1800)
        times += `<time style="left:${(offset / 10800) * 100}%">${clock(start + offset)}</time>`;
      body = `<div class="guide-grid"><div class="guide-heading"><span>Channel</span><div class="guide-hours">${times}</div></div>${body}</div>`;
    }
    $("channel-list").innerHTML = body;
    $("channel-list")
      .querySelectorAll("img")
      .forEach(
        (img) =>
          (img.onerror = () => {
            const el = img.parentElement;
            el.textContent =
              el
                .closest("[data-channel]")
                ?.getAttribute("aria-label")
                ?.split(" ")[0] || "";
          }),
      );
    renderGroups();
  }
  function closeDetail() {
    stopPlayer();
    state.selected = null;
    document.body.classList.remove("detail-open");
    $("detail").innerHTML =
      '<div class="player-placeholder">' +
      icon("tv") +
      "<h2>Select a channel</h2><p>See what’s on and start watching.</p></div>";
    renderChannels();
  }
  function selectChannel(c, p) {
    state.selected = c;
    if (!p) p = nowProgramme(c);
    document.body.classList.add("detail-open");
    const active = state.media && $("video");
    if (!active)
      $("detail").innerHTML =
        '<div class="detail-header"><span>Live TV</span><div class="actions"><button id="stop" hidden>Stop</button><button id="player-options-open" type="button">Options</button><button class="icon-button" id="detail-close" aria-label="Close channel details">' +
        icon("close") +
        '</button></div></div><video id="video" controls playsinline hidden></video><div id="play-status" role="status" hidden></div><div class="detail-body" id="detail-body"></div>';
    $("detail-body").innerHTML =
      `<h2>${esc(c.name)}</h2><span class="detail-channel">${esc(c.number)} · ${esc(c.sources.map((s) => s.name).join(" · "))}</span><div class="detail-programme">${p ? `<strong>${esc(p.title)}</strong><time>${clock(p.startUnix)}–${clock(p.endUnix)}</time><p>${esc(p.summary || "")}</p>` : "<strong>Programme information unavailable</strong><p>You can still watch this channel live.</p>"}</div><div class="actions"><button class="primary" id="watch" ${c.protected ? "disabled" : ""}>${icon("play")}${c.protected ? "Protected channel" : "Watch live"}</button></div>`;
    $("detail-body").insertAdjacentHTML("beforeend",`<div class="player-tools"><button type="button" data-player-mode="compatible" ${c.protected?"disabled":""}>Compatible playback</button><button type="button" data-player-mode="rewind" ${c.protected?"disabled":""}>Live Rewind</button><button type="button" id="add-multiview" ${c.protected?"disabled":""}>Add to multiview</button><details><summary>Player options</summary><div class="player-options"><button type="button" data-video-action="previous">Previous channel</button><button type="button" data-video-action="next">Next channel</button><button type="button" data-video-action="rewind">Back 30 seconds</button><button type="button" data-video-action="forward">Forward 30 seconds</button><button type="button" data-video-action="live">Go live</button><button type="button" data-video-action="aspect">Fit / fill screen</button><button type="button" data-video-action="pip">Picture in Picture</button><button type="button" data-video-action="fullscreen">Fullscreen</button><label>Audio track<select id="audio-track"><option value="0">Default audio</option></select></label></div></details></div>`);
    $("detail-body").querySelectorAll("[data-player-mode]").forEach(b=>b.onclick=()=>play(c,b.dataset.playerMode));
    $("detail-body").querySelectorAll("[data-video-action]").forEach(b=>b.onclick=()=>videoAction(b.dataset.videoAction));
    $("add-multiview").onclick=()=>addTile(c);
    $("player-options-open").onclick=()=>{const tools=$("detail-body").querySelector(".player-tools");tools.querySelector("details").open=true;tools.scrollIntoView({block:"nearest"});};
    $("audio-track").onchange=event=>{const n=Number(event.target.value);if(state.media?.hls)state.media.hls.audioTrack=n;else if($("video").audioTracks)Array.from($("video").audioTracks).forEach((track,i)=>track.enabled=i===n);};
    $("watch").onclick = () => play(c);
    $("stop").onclick = () => stopPlayer();
    $("detail-close").onclick = closeDetail;
    renderChannels();
  }
  const libraryPromises={};
  function loadScript(name) {
    if(libraryPromises[name])return libraryPromises[name];
    libraryPromises[name]=(async()=>{
      const r=await fetchAuth(base+"/assets/"+name);if(!r.ok)throw new Error("Unable to load the video player.");
      const blob=new Blob([await r.text()],{type:"text/javascript"});const url=URL.createObjectURL(blob);
      try{await new Promise((resolve,reject)=>{const script=document.createElement("script");script.src=url;script.onload=resolve;script.onerror=()=>{script.remove();reject(new Error("Unable to load the video player."))};document.head.append(script);});}finally{URL.revokeObjectURL(url)}
    })().catch(error=>{delete libraryPromises[name];throw error});return libraryPromises[name];
  }
  function playerStatus(message) {
    if ($("play-status")) {
      $("play-status").textContent = message;
      $("play-status").hidden = !message;
    }
  }
  async function stopPlayer() {
    state.generation++;state.session="";
    const media=state.media;state.media=null;
    if(media)await media.stop();
    if($("video"))$("video").hidden=true;
    if($("stop"))$("stop").hidden=true;
    playerStatus("");
  }
  function mediaOptions(video,status,error) {
    return {base,root,video,headers,fetch:fetchAuth,
      loadLibraries:()=>Promise.all([loadScript("hls.min.js"),loadScript("mpegts.min.js")]),
      onStatus:status,onError:error};
  }
  function refreshAudio() {
    const select=$("audio-track"),video=$("video");if(!select||!video)return;
    const tracks=state.media?.hls?.audioTracks || Array.from(video.audioTracks||[]);
    select.innerHTML=tracks.length?tracks.map((track,i)=>`<option value="${i}">${esc(track.name||track.label||track.lang||"Audio "+(i+1))}</option>`).join(""):'<option value="0">Default audio</option>';
  }
  async function play(c,mode="direct") {
    await stopTiles();
    const stopping=stopPlayer();const generation=state.generation;await stopping;if(generation!==state.generation)return;selectChannel(c);
    const video=$("video");video.hidden=false;$("stop").hidden=false;state.session="shared";
    const options=mediaOptions(video,playerStatus,playerStatus);
    const media=new SiloLiveMedia({...options,onAttached:()=>{if(generation!==state.generation)return;refreshAudio();remember(c.id);}});
    state.media=media;
    video.addEventListener("loadedmetadata",refreshAudio,{once:true});
    await media.play(c.id,mode);
  }
  async function videoAction(action) {
    const video=$("video");if(!video)return;
    const seek=()=>video.seekable?.length?[video.seekable.start(0),video.seekable.end(video.seekable.length-1)]:null;
    try {
      if(action==="previous"||action==="next") {const list=visible().filter(c=>!c.protected),index=list.findIndex(c=>c.id===state.selected?.id);const next=list[(index+(action==="next"?1:list.length-1))%list.length];if(next)play(next);}
      if(action==="rewind"||action==="forward"){const range=seek();if(range)video.currentTime=Math.max(range[0],Math.min(range[1]-.25,video.currentTime+(action==="rewind"?-30:30)));}
      if(action==="live"){const range=seek();if(range)video.currentTime=Math.max(range[0],range[1]-3);await video.play();}
      if(action==="aspect")video.style.objectFit=video.style.objectFit==="cover"?"contain":"cover";
      if(action==="pip") {if(document.pictureInPictureElement)await document.exitPictureInPicture();else if(video.requestPictureInPicture)await video.requestPictureInPicture();else playerStatus("Picture in Picture is unavailable in this browser.");}
      if(action==="fullscreen"){if(document.fullscreenElement)await document.exitFullscreen();else await video.requestFullscreen();}
    }catch(error){playerStatus(error.message)}
  }
  async function addTile(channel) {
    const viewer=profile();
    await stopPlayer();
    if(state.tiles.size>=4){notice("Multiview supports up to four channels.");return}
    if(state.tiles.has(channel.id)){notice("This channel is already in multiview.");return}
    const tile=document.createElement("article");tile.className="multiview-tile";
    tile.innerHTML=`<header><strong>${esc(channel.name)}</strong><button type="button" data-audio aria-label="Listen to ${esc(channel.name)}">Audio</button><button type="button" data-remove aria-label="Remove ${esc(channel.name)}">${icon("close")}</button></header><video controls playsinline muted></video><p role="status"></p>`;
    $("multiview-grid").append(tile);$("multiview").hidden=false;
    const video=tile.querySelector("video");video.muted=true;
    const label=tile.querySelector("p");
    const media=new SiloLiveMedia(mediaOptions(video,message=>label.textContent=message,message=>label.textContent=message));
    state.tiles.set(channel.id,{tile,media,video});
    tile.querySelector("[data-remove]").onclick=()=>removeTile(channel.id);
    tile.querySelector("[data-audio]").onclick=()=>{for(const item of state.tiles.values())item.video.muted=item.video!==video;};
    await media.play(channel.id);if(viewer===profile()&&state.tiles.has(channel.id))remember(channel.id);
  }
  async function removeTile(id){const item=state.tiles.get(id);if(!item)return;state.tiles.delete(id);item.tile.remove();$("multiview").hidden=state.tiles.size===0;await item.media.stop();}
  function stopTiles(){return Promise.all(Array.from(state.tiles.keys(),removeTile));}
  $("multiview-close").onclick=stopTiles;
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
 state.recent=Array.isArray(view.recentChannels)?view.recentChannels:[];
  }
  function updatePreferences(change) {
    const viewer=profile();
    state.write=state.write.catch(()=>{}).then(async()=>{
      const path="/settings/plugins/"+installation;
      const payload=await api(path,"GET",undefined,true);
      if(viewer!==profile())throw new Error("Profile changed. Reload before saving.");
      const values=payload.values||{};
      const raw=typeof values.preferences==="string"?JSON.parse(values.preferences||"{}"):values.preferences||{};
      let target=raw;
      if(viewer){raw.profiles||={};raw.profiles[viewer]||={favorites:{...(raw.favorites||{})},favoriteOrder:[...(raw.favoriteOrder||[])],recentChannels:[...(raw.recentChannels||[])]};target=raw.profiles[viewer];}
      change(target);values.preferences=JSON.stringify(raw);
      await api(path,"PUT",{values},true);
      if(viewer!==profile())return;
      state.favorites=target.favorites||{};state.recent=target.recentChannels||[];renderChannels();
    }).catch(error=>notice("Could not save preferences: "+error.message));
  }
  function favorite(id){const value=!state.favorites[id];updatePreferences(target=>{target.favorites={...(target.favorites||{})};if(value)target.favorites[id]=true;else delete target.favorites[id];target.favoriteOrder=(target.favoriteOrder||[]).filter(x=>x!==id);if(value)target.favoriteOrder.push(id);});}
  function remember(id){updatePreferences(target=>{target.recentChannels=[id,...(target.recentChannels||[]).filter(x=>x!==id)].slice(0,20);});}
  function renderRecent(){const root=$("recent-channels");root.hidden=!state.onlyFavorites||state.recent.length===0;root.innerHTML='<h2>Recently watched</h2><div class="recent-list">'+state.recent.map(id=>state.channels.find(c=>c.id===id)).filter(Boolean).map(c=>`<button type="button" data-recent="${esc(c.id)}">${logo(c)}<span>${esc(c.name)}</span></button>`).join("")+"</div>";root.querySelectorAll("[data-recent]").forEach(button=>button.onclick=()=>{const channel=state.channels.find(c=>c.id===button.dataset.recent);if(channel)play(channel);});}
  async function loadGuide() {
    const start = Math.floor(Date.now() / 1000 / 10800) * 10800 + state.offset;
    const ids = pageChannels().map((c) => c.id);
    const key = JSON.stringify([start, ids]);
    state.guideRequest = key;
    if (!ids.length) {
      state.programs = [];
      renderChannels();
      return;
    }
    const result = await api(
      "/api/guide?start=" +
        start +
        "&end=" +
        (start + 10800) +
        "&channel_ids=" +
        encodeURIComponent(ids.join(",")),
    );
    if (state.guideRequest !== key) return;
    state.programs = result.programs || [];
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
    const theme = encodeURIComponent(
      document.documentElement.dataset.siloTheme || "midnight-cinema",
    );
    $("devices-tab").href = base + "/admin?theme=" + theme;
    $("manage-devices").hidden = !data.admin;
    $("manage-devices").href = base + "/admin?theme=" + theme;
    $("admin-channels").href = base + "?theme=" + theme + "#channels";
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
            return `<article class="device"><div><h3>${esc(e.name || e.id)}</h3><p>${esc(e.url)}</p><p>${esc(health?.model || "HDHomeRun-compatible endpoint")}</p></div><div class="device-health"><p class="${health?.error ? "error" : "health"}">${e.enabled === false ? "Disabled" : health?.error ? esc(health.error) : health?.lastSuccessUnix ? "Connected" : "Waiting for discovery"}</p><p>${health?.channelCount || 0} channels · ${health?.inUse || 0} / ${health?.tunerCount || "—"} tuners reserved</p>${health?.capacityGroup?`<p>Server ${esc(health.capacityGroup)}: ${health.sharedInUse||0} / ${health.sharedTunerLimit} shared tuners</p>`:""}${health?.guideError ? `<p class="error">Guide: ${esc(health.guideError)}</p>` : e.guide_mode && e.guide_mode !== "none" ? `<p>${health?.programCount || 0} programmes</p>` : "<p>No guide configured</p>"}</div><div class="actions"><button data-edit="${i}">Edit</button><button data-remove="${i}">Remove</button></div></article>`;
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
 "capacity_group",
 "shared_tuner_limit",
      "priority",
      "guide_mode",
      "guide_url",
      "guide_url_file",
    ])
      form.elements[name].value =
        e[name] ?? (name === "tuner_limit" || name === "priority" || name === "shared_tuner_limit" ? 0 : "");
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
 capacity_group:form.elements.capacity_group.value.trim(),
 shared_tuner_limit:Number(form.elements.shared_tuner_limit.value),
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
  let searchTimer;
  $("search").oninput = (event) => {
    state.search = event.target.value.toLowerCase();
    state.page = 0;
    renderChannels();
    clearTimeout(searchTimer);
    searchTimer = setTimeout(
      () => loadGuide().catch((e) => notice(e.message)),
      200,
    );
  };
  function chooseSource(id) {
    state.source = id;
    state.page = 0;
    state.onlyFavorites = false;
    $("source").value = id;
    if (location.hash === "#favorites") location.hash = "#channels";
    renderChannels();
    loadGuide().catch((e) => notice(e.message));
  }
  $("source").onchange = (event) => chooseSource(event.target.value);
  $("source-groups").onclick = (event) => {
    const button = event.target.closest("[data-source]");
    if (button) chooseSource(button.dataset.source);
  };
  $("all-channels").onclick = () => chooseSource("");
  $("favorites").onclick = () => {
    state.source = "";
    $("source").value = "";
    if (location.hash === "#favorites") {
      state.onlyFavorites = true;
      state.page = 0;
      renderChannels();
      loadGuide().catch((e) => notice(e.message));
    } else location.hash = "#favorites";
  };
  for (const [id, delta] of [
    ["page-previous", -1],
    ["page-next", 1],
  ])
    $(id).onclick = () => {
      state.page = Math.max(0, state.page + delta);
      $("channel-scroll").scrollTop = 0;
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
    state.onlyFavorites = location.hash === "#favorites";
    state.page = 0;
    renderChannels();
    loadGuide().catch((e) => notice(e.message));
  };
  window.addEventListener("pagehide", () => {stopPlayer();stopTiles();});
  window.addEventListener("storage", (event) => {
    if (event.key === "profile_id") {
 stopTiles();
      stopPlayer()
        .then(() => preferences())
        .then(() => renderChannels())
        .catch((e) => notice(e.message));
    }
  });
  async function start() {
    state.onlyFavorites = location.hash === "#favorites";
    const theme = encodeURIComponent(
      document.documentElement.dataset.siloTheme || "midnight-cinema",
    );
    for (const [id, file] of [
      ["hls-license", "hls.LICENSE"],
      ["mpegts-license", "mpegts.LICENSE"],
      ["font-license", "font.LICENSE"],
    ])
      $(id).href = base + "/assets/" + file;
    document.body.classList.toggle("is-admin", adminPage);
    $("admin").hidden = !adminPage;
    $("viewer").hidden = adminPage;
    if (adminPage) {
      $("channels-tab").href = base + "?theme=" + theme + "#channels";
      $("guide-tab").href = base + "?theme=" + theme + "#guide";
      $("favorites-tab").href = base + "?theme=" + theme + "#favorites";
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
