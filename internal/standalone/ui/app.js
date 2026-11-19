(() => {
  "use strict";

  const app = {
    csrf: "",
    state: null,
    providers: [],
    models: [],
    modelsLoaded: false,
    usage: null,
    usageLoaded: false,
    hostAgents: [],
    localSources: [],
    hostLoaded: false,
    routingGroups: [],
    routingGroupsLoaded: false,
    clientKeys: [],
    localKeyText: "",
    createdKeyText: "",
    noticeTimer: 0,
    cooldownTimer: 0,
    cooldownItems: [],
  };

  const ui = {};

  class ApiError extends Error {
    constructor(code, message, status = 0) {
      super(message || code || "request_failed");
      this.name = "ApiError";
      this.code = code || "request_failed";
      this.status = status;
    }
  }

  function init() {
    cacheElements();
    bindEvents();
    void boot();
  }

  function cacheElements() {
    const get = (id) => {
      const node = document.getElementById(id);
      if (!node) {
        throw new Error(`missing element: ${id}`);
      }
      return node;
    };

    Object.assign(ui, {
      globalAlert: get("global-alert"),
      globalAlertText: get("global-alert-text"),
      alertDismiss: get("alert-dismiss"),
      globalNotice: get("global-notice"),
      loginView: get("login-view"),
      loginForm: get("login-form"),
      adminToken: get("admin-token"),
      loginError: get("login-error"),
      loginSubmit: get("login-submit"),
      consoleView: get("console-view"),
      logoutButton: get("logout-button"),
      refreshState: get("refresh-state"),
      stateTimestamp: get("state-timestamp"),
      statReady: get("stat-ready"),
      statReadyHint: get("stat-ready-hint"),
      statRepair: get("stat-repair"),
      statRepairHint: get("stat-repair-hint"),
      statGeneration: get("stat-generation"),
      statGenerationHint: get("stat-generation-hint"),
      statProviders: get("stat-providers"),
      statProvidersHint: get("stat-providers-hint"),
      statModels: get("stat-models"),
      statModelsHint: get("stat-models-hint"),
      statKeys: get("stat-keys"),
      statKeysHint: get("stat-keys-hint"),
      lastErrorWrap: get("last-error-wrap"),
      lastError: get("last-error"),
      refreshUsage: get("refresh-usage"),
      scanAgents: get("scan-agents"),
      scanLocalModels: get("scan-local-models"),
      hostStatus: get("host-status"),
      hostAgentsBody: get("host-agents-body"),
      localModelsBody: get("local-models-body"),
      localModelImportForm: get("local-model-import-form"),
      localModelKind: get("local-model-kind"),
      localModelBaseUrl: get("local-model-base-url"),
      localModelSlug: get("local-model-slug"),
      localModelName: get("local-model-name"),
      localModelConfirm: get("local-model-confirm"),
      localModelImportSubmit: get("local-model-import-submit"),
      usageTimestamp: get("usage-timestamp"),
      usageError: get("usage-error"),
      usageMinuteRequests: get("usage-minute-requests"),
      usageDayRequests: get("usage-day-requests"),
      usageDayTokens: get("usage-day-tokens"),
      usageBody: get("usage-body"),
      cooldownsBody: get("cooldowns-body"),
      localKeyWrap: get("local-key-wrap"),
      localKeyOnce: get("local-key-once"),
      copyLocalKey: get("copy-local-key"),
      dismissLocalKey: get("dismiss-local-key"),
      refreshProviders: get("refresh-providers"),
      providerForm: get("provider-form"),
      providerSlug: get("provider-slug"),
      providerName: get("provider-name"),
      providerBaseUrl: get("provider-base-url"),
      providerKind: get("provider-kind"),
      providerAuthType: get("provider-auth-type"),
      providerApiKey: get("provider-api-key"),
      providerTlsInsecure: get("provider-tls-insecure"),
      providerSubmit: get("provider-submit"),
      providerActionStatus: get("provider-action-status"),
      providerResult: get("provider-result"),
      providersBody: get("providers-body"),
      refreshModels: get("refresh-models"),
      modelsBody: get("models-body"),
      refreshRoutingGroups: get("refresh-routing-groups"),
      routingGroupsError: get("routing-groups-error"),
      routingGroupForm: get("routing-group-form"),
      routingGroupName: get("routing-group-name"),
      routingGroupDescription: get("routing-group-description"),
      routingGroupEnabled: get("routing-group-enabled"),
      routingGroupSubmit: get("routing-group-submit"),
      routingModelPicker: get("routing-model-picker"),
      routingGroupsBody: get("routing-groups-body"),
      refreshKeys: get("refresh-keys"),
      clientKeyForm: get("client-key-form"),
      clientKeyName: get("client-key-name"),
      clientKeyRpm: get("client-key-rpm"),
      clientKeyDailyRequests: get("client-key-daily-requests"),
      clientKeyDailyTokens: get("client-key-daily-tokens"),
      clientKeyAllowlist: get("client-key-allowlist"),
      clientKeySubmit: get("client-key-submit"),
      createdKeyWrap: get("created-key-wrap"),
      createdKeyPlaintext: get("created-key-plaintext"),
      copyCreatedKey: get("copy-created-key"),
      dismissCreatedKey: get("dismiss-created-key"),
      clientKeysBody: get("client-keys-body"),
    });
  }

  function bindEvents() {
    ui.loginForm.addEventListener("submit", handleLogin);
    ui.adminToken.addEventListener("input", hideLoginError);
    ui.logoutButton.addEventListener("click", handleLogout);
    ui.alertDismiss.addEventListener("click", hideError);

    ui.refreshState.addEventListener("click", () => {
      void runAction(
        ui.refreshState,
        async () => {
          await loadState();
          showNotice("运行状态已刷新。");
        },
        "刷新中…",
      );
    });

    ui.refreshUsage.addEventListener("click", () => {
      void runAction(
        ui.refreshUsage,
        async () => {
          await loadUsage({ throwOnError: true });
          showNotice("用量与冷却已刷新。");
        },
        "刷新中…",
      );
    });

    ui.scanAgents.addEventListener("click", () => {
      void runAction(
        ui.scanAgents,
        async () => {
          await scanHostAgents();
          showNotice("本机 Agent 扫描完成。");
        },
        "扫描中…",
      );
    });

    ui.scanLocalModels.addEventListener("click", () => {
      void runAction(
        ui.scanLocalModels,
        async () => {
          await scanLocalModels();
          showNotice("本地模型发现完成。");
        },
        "发现中…",
      );
    });

    ui.localModelImportForm.addEventListener("submit", handleLocalModelImport);

    ui.providerForm.addEventListener("submit", handleProviderCreate);
    ui.refreshProviders.addEventListener("click", () => {
      void runAction(
        ui.refreshProviders,
        async () => {
          await loadProviders({ throwOnError: true });
          showNotice("服务商列表已刷新。");
        },
        "刷新中…",
      );
    });

    ui.refreshModels.addEventListener("click", () => {
      void runAction(
        ui.refreshModels,
        async () => {
          await loadModels({ throwOnError: true });
          showNotice("模型列表已刷新。");
        },
        "刷新中…",
      );
    });

    ui.routingGroupForm.addEventListener("submit", handleRoutingGroupCreate);
    ui.refreshRoutingGroups.addEventListener("click", () => {
      void runAction(
        ui.refreshRoutingGroups,
        async () => {
          await loadRoutingGroups({ throwOnError: true });
          showNotice("路由组列表已刷新。");
        },
        "刷新中…",
      );
    });

    ui.clientKeyForm.addEventListener("submit", handleClientKeyCreate);
    ui.refreshKeys.addEventListener("click", () => {
      void runAction(
        ui.refreshKeys,
        async () => {
          await loadClientKeys({ throwOnError: true });
          showNotice("客户端 Key 列表已刷新。");
        },
        "刷新中…",
      );
    });

    ui.copyLocalKey.addEventListener("click", () => {
      void copyText(app.localKeyText);
    });
    ui.copyCreatedKey.addEventListener("click", () => {
      void copyText(app.createdKeyText);
    });
    ui.dismissLocalKey.addEventListener("click", dismissLocalKey);
    ui.dismissCreatedKey.addEventListener("click", dismissCreatedKey);
  }

  async function boot() {
    try {
      const payload = await request("/admin/state");
      if (isObject(payload) && payload.authenticated === false) {
        showLogin(false);
        return;
      }
      applyState(payload);
      enterConsole();
      await loadAllData();
    } catch (error) {
      const authFailure =
        error.status === 401 ||
        (error.status === 403 && String(error.code || "").includes("auth"));
      showLogin(false);
      if (!authFailure) {
        showError(error);
      }
    }
  }

  async function handleLogin(event) {
    event.preventDefault();
    hideLoginError();
    hideError();
    hideNotice();

    const adminToken = ui.adminToken.value.trim();
    if (!adminToken) {
      showLoginError("请输入管理 Token。");
      return;
    }

    setButtonBusy(ui.loginSubmit, true, "正在登录…");
    try {
      const loginPayload = await request("/admin/session", {
        method: "POST",
        body: { token: adminToken },
      });
      rememberCsrf(loginPayload);

      let statePayload = inlineState(loginPayload);
      if (!statePayload) {
        statePayload = await request("/admin/state");
      }

      applyState(statePayload);
      ui.adminToken.value = "";
      enterConsole();
      showNotice("登录成功。");
      await loadAllData();
    } catch (error) {
      showLoginError(errorText(error));
    } finally {
      setButtonBusy(ui.loginSubmit, false);
    }
  }

  async function handleLogout() {
    await runAction(
      ui.logoutButton,
      async () => {
        try {
          await api("/admin/session", { method: "DELETE" });
        } catch (error) {
          if (error.status !== 401) {
            throw error;
          }
        }
        showLogin(true);
        hideError();
        showNotice("已退出登录。");
      },
      "正在退出…",
    );
  }

  function enterConsole() {
    ui.loginView.hidden = true;
    ui.consoleView.hidden = false;
    ui.logoutButton.hidden = false;
    hideLoginError();
    document.title = "管理控制台 · AgentMesh";
  }

  function showLogin(focusToken = true, preserveSecrets = false) {
    app.csrf = "";
    ui.consoleView.hidden = true;
    ui.loginView.hidden = false;
    ui.logoutButton.hidden = true;
    ui.adminToken.value = "";
    hideLoginError();
    hideNotice();
    stopCooldownTicker();
    document.title = "AgentMesh 管理控制台";

    if (!preserveSecrets) {
      dismissLocalKey();
      dismissCreatedKey();
    }

    if (focusToken) {
      window.requestAnimationFrame(() => ui.adminToken.focus());
    }
  }

  async function loadAllData() {
    await Promise.allSettled([
      loadUsage(),
      scanHostAgents(),
      scanLocalModels(),
      loadProviders(),
      loadModels(),
      loadRoutingGroups(),
      loadClientKeys(),
    ]);
    if (app.usageLoaded) {
      renderUsage();
    }
    if (app.modelsLoaded) {
      renderModels();
      renderRoutingModelPicker();
    }
    if (app.routingGroupsLoaded) {
      renderRoutingGroups();
    }
  }

  async function loadState() {
    const payload = await request("/admin/state");
    if (isObject(payload) && payload.authenticated === false) {
      showLogin(false);
      throw new ApiError("admin_auth_failed", "管理会话已失效，请重新登录。", 401);
    }
    applyState(payload);
    return payload;
  }

  function applyState(payload) {
    rememberCsrf(payload);
    const state = stateObject(payload);
    app.state = state;
    renderState(state);
    return state;
  }

  function renderState(stateValue) {
    const state = isObject(stateValue) ? stateValue : {};
    const ready = readBoolean(state.ready);
    const repair = readBoolean(
      firstDefined(
        state.repair_required,
        state.repairRequired,
        state.repair,
      ),
    );
    const generation = firstNumber(
      state.generation,
      state.current_generation,
      state.snapshot_generation,
      state.snapshot?.generation,
    );

    const providerCounts = mergeCounts(
      state.providers,
      state.provider_stats,
      state.stats?.providers,
      state.counts?.providers,
    );
    const modelCounts = mergeCounts(
      state.models,
      state.model_stats,
      state.stats?.models,
      state.counts?.models,
    );
    const keyCounts = mergeCounts(
      state.keys,
      state.client_keys,
      state.client_key_stats,
      state.stats?.keys,
      state.stats?.client_keys,
      state.counts?.keys,
      state.counts?.client_keys,
    );

    const providerPrimary = firstNumber(
      providerCounts.total,
      state.provider_count,
      state.providerCount,
      state.counts?.provider_count,
      providerCounts.enabled,
    );
    const modelPrimary = firstNumber(
      modelCounts.total,
      state.model_count,
      state.modelCount,
      state.counts?.model_count,
      modelCounts.enabled,
    );
    const keyPrimary = firstNumber(
      keyCounts.total,
      state.key_count,
      state.client_key_count,
      state.counts?.key_count,
      keyCounts.enabled,
      state.local_key ? 1 : null,
    );

    setStat(
      ui.statReady,
      ui.statReadyHint,
      ready === null ? "—" : ready ? "是" : "否",
      ready === null ? [] : [ready ? "数据面可服务" : "数据面未就绪"],
    );
    setStat(
      ui.statRepair,
      ui.statRepairHint,
      repair === null ? "—" : repair ? "需要" : "无需",
      repair === null ? [] : [repair ? "请检查配置错误" : "快照编译正常"],
    );
    setStat(
      ui.statGeneration,
      ui.statGenerationHint,
      generation === null ? "—" : generation,
      generation === null ? [] : ["当前快照代次"],
    );
    setStat(
      ui.statProviders,
      ui.statProvidersHint,
      providerPrimary,
      countHints(providerCounts, "启用", { unreadable: "不可读" }),
    );
    setStat(
      ui.statModels,
      ui.statModelsHint,
      modelPrimary,
      countHints(modelCounts, "启用", { stale: "已移除" }),
    );
    setStat(
      ui.statKeys,
      ui.statKeysHint,
      keyPrimary,
      countHints(keyCounts, "启用", { revoked: "已撤销" }),
    );

    ui.stateTimestamp.textContent = `更新于 ${formatClock(new Date())}`;

    const lastError = firstDefined(state.last_error, state.lastError);
    if (isMissing(lastError)) {
      ui.lastErrorWrap.hidden = true;
      ui.lastError.textContent = "";
    } else {
      ui.lastError.textContent = formatErrorValue(lastError);
      ui.lastErrorWrap.hidden = false;
    }

    if (
      Object.prototype.hasOwnProperty.call(state, "local_key_once") &&
      !isMissing(state.local_key_once)
    ) {
      const localKey = oneTimeText(state.local_key_once);
      if (localKey) {
        app.localKeyText = localKey;
        ui.localKeyOnce.textContent = localKey;
        ui.localKeyWrap.hidden = false;
      }
    }
  }

  function countHints(counts, enabledLabel, labels = {}) {
    const hints = [];
    if (counts.enabled !== null && counts.enabled !== undefined) {
      hints.push(`${enabledLabel} ${counts.enabled}`);
    }
    if (counts.unreadable) {
      hints.push(`${labels.unreadable || "不可读"} ${counts.unreadable}`);
    }
    if (counts.stale) {
      hints.push(`${labels.stale || "stale"} ${counts.stale}`);
    }
    if (counts.revoked) {
      hints.push(`${labels.revoked || "已撤销"} ${counts.revoked}`);
    }
    return hints;
  }

  function setStat(valueNode, hintNode, value, hints) {
    valueNode.textContent =
      value === null || value === undefined ? "—" : String(value);
    hintNode.textContent = Array.isArray(hints) && hints.length > 0
      ? hints.join(" · ")
      : "";
  }

  async function loadProviders(options = {}) {
    try {
      const payload = await request("/admin/providers");
      app.providers = extractList(payload, ["providers"]);
      renderProviders();
      if (app.modelsLoaded) {
        renderModels();
      }
      if (app.usageLoaded) {
        renderUsage();
      }
      return true;
    } catch (error) {
      app.providers = [];
      renderTableError(ui.providersBody, 5, error);
      if (options.throwOnError) {
        throw error;
      }
      return false;
    }
  }

  async function loadModels(options = {}) {
    try {
      const payload = await request("/admin/models");
      app.models = extractList(payload, ["models", "model_routes"]);
      app.modelsLoaded = true;
      renderModels();
      renderRoutingModelPicker();
      if (app.routingGroupsLoaded) {
        renderRoutingGroups();
      }
      return true;
    } catch (error) {
      app.models = [];
      app.modelsLoaded = false;
      renderTableError(ui.modelsBody, 6, error);
      renderRoutingModelPicker();
      if (options.throwOnError) {
        throw error;
      }
      return false;
    }
  }

  async function loadClientKeys(options = {}) {
    try {
      const payload = await request("/admin/client-keys");
      app.clientKeys = extractList(payload, [
        "client_keys",
        "clientKeys",
        "keys",
      ]);
      renderClientKeys();
      if (app.usageLoaded) {
        renderUsage();
      }
      return true;
    } catch (error) {
      app.clientKeys = [];
      renderTableError(ui.clientKeysBody, 6, error);
      if (options.throwOnError) {
        throw error;
      }
      return false;
    }
  }

  async function loadUsage(options = {}) {
    try {
      const payload = await request("/admin/usage");
      app.usage = isObject(payload) ? payload : {};
      app.usageLoaded = true;
      ui.usageError.hidden = true;
      ui.usageError.textContent = "";
      renderUsage();
      return true;
    } catch (error) {
      app.usage = null;
      app.usageLoaded = false;
      renderUsageError(error);
      if (options.throwOnError) {
        throw error;
      }
      return false;
    }
  }

  async function scanHostAgents() {
    try {
      const payload = await api("/admin/host/scan", {
        method: "POST",
        body: {},
      });
      app.hostAgents = extractList(payload, ["agents"]);
      app.hostLoaded = true;
      renderHostAgents(payload);
      return true;
    } catch (error) {
      app.hostAgents = [];
      app.hostLoaded = true;
      renderTableError(ui.hostAgentsBody, 3, error);
      ui.hostStatus.textContent = `扫描失败：${errorText(error)}`;
      throw error;
    }
  }

  async function scanLocalModels() {
    try {
      const payload = await api("/admin/host/local-models/scan", {
        method: "POST",
        body: {},
      });
      app.localSources = extractList(payload, ["sources"]);
      renderLocalSources();
      return true;
    } catch (error) {
      app.localSources = [];
      renderTableError(ui.localModelsBody, 3, error);
      ui.hostStatus.textContent = `本地模型发现失败：${errorText(error)}`;
      throw error;
    }
  }

  async function handleLocalModelImport(event) {
    event.preventDefault();
    hideError();
    hideNotice();
    if (!ui.localModelConfirm.checked) {
      showError(
        new ApiError(
          "local_model_import_confirm_required",
          "请先勾选确认后再导入本地模型目录。",
        ),
      );
      return;
    }
    const body = {
      kind: ui.localModelKind.value,
      base_url: ui.localModelBaseUrl.value.trim(),
      slug: ui.localModelSlug.value.trim(),
      name: ui.localModelName.value.trim(),
      confirm: true,
    };
    setButtonBusy(ui.localModelImportSubmit, true, "导入中…");
    try {
      const payload = await api("/admin/host/local-models/import", {
        method: "POST",
        body,
      });
      const provider = isObject(payload?.provider) ? payload.provider : {};
      await Promise.allSettled([loadProviders(), loadModels()]);
      ui.hostStatus.textContent = `已导入 ${provider.name || provider.slug || "本地来源"}；模型默认禁用。`;
      showNotice("本地模型目录已导入。");
    } catch (error) {
      showError(error);
    } finally {
      setButtonBusy(ui.localModelImportSubmit, false);
    }
  }

  async function loadRoutingGroups(options = {}) {
    try {
      const payload = await request("/admin/routing-groups");
      app.routingGroups = extractList(payload, [
        "routing_groups",
        "routingGroups",
        "groups",
      ]);
      app.routingGroupsLoaded = true;
      ui.routingGroupsError.hidden = true;
      ui.routingGroupsError.textContent = "";
      renderRoutingGroups();
      return true;
    } catch (error) {
      app.routingGroups = [];
      app.routingGroupsLoaded = false;
      ui.routingGroupsError.textContent = errorText(error);
      ui.routingGroupsError.hidden = false;
      renderTableError(ui.routingGroupsBody, 5, error);
      if (options.throwOnError) {
        throw error;
      }
      return false;
    }
  }

  function renderUsage() {
    stopCooldownTicker();
    app.cooldownItems = [];

    const usage = isObject(app.usage) ? app.usage : {};
    const windowSummary = isObject(usage.window) ? usage.window : {};
    ui.usageMinuteRequests.textContent = countText(
      firstNumber(windowSummary.minute_requests),
    );
    ui.usageDayRequests.textContent = countText(
      firstNumber(windowSummary.day_requests),
    );
    ui.usageDayTokens.textContent = countText(
      firstNumber(windowSummary.day_tokens),
    );
    ui.usageTimestamp.textContent = `更新于 ${formatClock(new Date())}`;

    const rows = Array.isArray(usage.rows) ? usage.rows : [];
    if (rows.length === 0) {
      renderEmpty(ui.usageBody, 6, "当前窗口暂无用量记录。");
    } else {
      const fragment = document.createDocumentFragment();
      for (const rowValue of rows) {
        const row = isObject(rowValue) ? rowValue : {};
        const keyID = firstText(row.key_id, row.keyId, row.key);
        const providerID = firstText(
          row.provider_id,
          row.providerId,
          row.provider,
        );
        const key = clientKeyIdentity(keyID);
        const provider = providerIdentity(providerID);
        const tableRow = document.createElement("tr");
        tableRow.append(
          stackCell(key.title, key.meta),
          stackCell(provider.title, provider.meta),
          codeCell(firstText(row.model, row.public_id, "—")),
          numberCell(firstNumber(row.requests, row.count)),
          numberCell(firstNumber(row.input_tokens, row.inputTokens)),
          numberCell(firstNumber(row.output_tokens, row.outputTokens)),
        );
        fragment.append(tableRow);
      }
      ui.usageBody.replaceChildren(fragment);
    }

    const cooldowns = Array.isArray(usage.cooldowns) ? usage.cooldowns : [];
    if (cooldowns.length === 0) {
      renderEmpty(ui.cooldownsBody, 5, "当前没有活跃冷却。");
    } else {
      const fragment = document.createDocumentFragment();
      for (const cooldownValue of cooldowns) {
        const cooldown = isObject(cooldownValue) ? cooldownValue : {};
        const providerID = firstText(
          cooldown.provider_id,
          cooldown.providerId,
          cooldown.provider,
        );
        const provider = providerIdentity(providerID);
        const untilMs = parseTimeMillis(cooldown.until);
        const tableRow = document.createElement("tr");
        const countdown = document.createElement("strong");
        countdown.className = "countdown";
        countdown.textContent = cooldownCountdown(untilMs, Date.now());
        const countdownCell = document.createElement("td");
        countdownCell.append(countdown);
        tableRow.append(
          stackCell(provider.title, provider.meta),
          codeCell(firstText(cooldown.model, "—")),
          countdownCell,
          textCell(formatDate(cooldown.until)),
          textCell(firstText(cooldown.reason, "—")),
        );
        app.cooldownItems.push({ node: countdown, untilMs });
        fragment.append(tableRow);
      }
      ui.cooldownsBody.replaceChildren(fragment);
      startCooldownTicker();
    }
  }

  function renderUsageError(error) {
    stopCooldownTicker();
    app.cooldownItems = [];
    ui.usageMinuteRequests.textContent = "—";
    ui.usageDayRequests.textContent = "—";
    ui.usageDayTokens.textContent = "—";
    ui.usageTimestamp.textContent = "用量加载失败。";
    ui.usageError.textContent = errorText(error);
    ui.usageError.hidden = false;
    renderTableError(ui.usageBody, 6, error);
    renderTableError(ui.cooldownsBody, 5, error);
  }

  function startCooldownTicker() {
    if (app.cooldownItems.length === 0) {
      return;
    }
    updateCooldownTicker();
    app.cooldownTimer = window.setInterval(updateCooldownTicker, 1000);
  }

  function stopCooldownTicker() {
    if (app.cooldownTimer) {
      window.clearInterval(app.cooldownTimer);
      app.cooldownTimer = 0;
    }
  }

  function updateCooldownTicker() {
    let active = false;
    const now = Date.now();
    for (const item of app.cooldownItems) {
      if (!item || !item.node) {
        continue;
      }
      const text = cooldownCountdown(item.untilMs, now);
      item.node.textContent = text;
      item.node.classList.toggle("is-expired", text === "已结束");
      if (text !== "已结束" && text !== "—") {
        active = true;
      }
    }
    if (!active) {
      stopCooldownTicker();
    }
  }

  function cooldownCountdown(untilMs, nowMs) {
    if (!Number.isFinite(untilMs)) {
      return "—";
    }
    const seconds = Math.ceil((untilMs - nowMs) / 1000);
    if (seconds <= 0) {
      return "已结束";
    }
    const hours = Math.floor(seconds / 3600);
    const minutes = Math.floor((seconds % 3600) / 60);
    const remainder = seconds % 60;
    if (hours > 0) {
      return `${hours}时${pad2(minutes)}分${pad2(remainder)}秒`;
    }
    if (minutes > 0) {
      return `${minutes}分${pad2(remainder)}秒`;
    }
    return `${remainder}秒`;
  }

  function renderProviders() {
    if (app.providers.length === 0) {
      renderEmpty(
        ui.providersBody,
        5,
        "暂无服务商。请在上方表单添加第一个 OpenAI 兼容服务商。",
      );
      return;
    }

    const fragment = document.createDocumentFragment();
    for (const providerValue of app.providers) {
      const provider = providerView(providerValue);
      const row = document.createElement("tr");

      const nameCell = document.createElement("td");
      const nameStack = document.createElement("div");
      nameStack.className = "cell-stack";
      const name = document.createElement("strong");
      name.className = "cell-title";
      name.textContent = provider.name;
      const meta = document.createElement("span");
      meta.className = "cell-meta";
      meta.textContent = provider.slug
        ? `slug: ${provider.slug}${provider.kind ? ` · ${provider.kind}` : ""}`
        : provider.id || "缺少内部 ID";
      nameStack.append(name, meta);
      nameCell.append(nameStack);

      const baseCell = document.createElement("td");
      baseCell.className = "url-cell";
      baseCell.textContent = provider.baseUrl || "—";

      const authCell = document.createElement("td");
      authCell.textContent = authLabel(provider.authType);
      if (provider.tlsInsecure) {
        authCell.append(document.createElement("br"), badge("TLS 跳过校验", true));
      }

      const statusCell = document.createElement("td");
      const statusBadges = document.createElement("div");
      statusBadges.className = "badge-stack";
      if (provider.enabled === true) {
        statusBadges.append(badge("启用"));
      } else if (provider.enabled === false) {
        statusBadges.append(badge("停用", true));
      } else {
        statusBadges.append(badge("状态未知", true));
      }
      if (provider.unreadable) {
        statusBadges.append(badge("凭据不可读", true));
      }
      statusCell.append(statusBadges);

      const actionsCell = document.createElement("td");
      const actions = document.createElement("div");
      actions.className = "row-actions";

      const testButton = rowButton("测试 /models", "");
      testButton.addEventListener("click", () => {
        void runAction(
          testButton,
          async () => {
            await testProvider(provider);
          },
          "测试中…",
          (error) => {
            setProviderActionStatus(
              `测试 /models 失败：${errorText(error)}`,
            );
          },
        );
      });

      const discoverButton = rowButton("发现模型", "");
      discoverButton.addEventListener("click", () => {
        void runAction(
          discoverButton,
          async () => {
            await discoverProvider(provider);
          },
          "发现中…",
          (error) => {
            setProviderActionStatus(
              `发现模型失败：${errorText(error)}`,
            );
          },
        );
      });

      const toggleButton = rowButton(
        provider.enabled === true ? "停用" : "启用",
        "button-quiet",
      );
      toggleButton.addEventListener("click", () => {
        void runAction(
          toggleButton,
          async () => {
            await toggleProvider(provider);
          },
          "更新中…",
          (error) => {
            setProviderActionStatus(
              `更新服务商失败：${errorText(error)}`,
            );
          },
        );
      });

      const deleteButton = rowButton("删除", "button-danger");
      deleteButton.addEventListener("click", () => {
        void runAction(
          deleteButton,
          async () => {
            await deleteProvider(provider);
          },
          "删除中…",
          (error) => {
            setProviderActionStatus(
              `删除服务商失败：${errorText(error)}`,
            );
          },
        );
      });

      actions.append(
        testButton,
        discoverButton,
        toggleButton,
        deleteButton,
      );
      actionsCell.append(actions);
      row.append(nameCell, baseCell, authCell, statusCell, actionsCell);
      fragment.append(row);
    }

    ui.providersBody.replaceChildren(fragment);
  }

  function renderHostAgents(payload) {
    const result = isObject(payload) ? payload : {};
    if (app.hostAgents.length === 0) {
      renderEmpty(
        ui.hostAgentsBody,
        3,
        "未发现当前的 Codex、Claude Code、Gemini CLI 或 OpenCode 安装。",
      );
    } else {
      const fragment = document.createDocumentFragment();
      for (const agentValue of app.hostAgents) {
        const agent = isObject(agentValue) ? agentValue : {};
        const paths = [];
        if (agent.executable_path) {
          paths.push(agent.executable_path);
        }
        if (Array.isArray(agent.config_paths)) {
          paths.push(...agent.config_paths);
        }
        const row = document.createElement("tr");
        row.append(
          stackCell(agent.name || agent.id, agent.id),
          statusCell(agent.status, agent.reason),
          stackCell(paths.length > 0 ? "已定位" : "未定位", paths.join(" · ") || "无"),
        );
        fragment.append(row);
      }
      ui.hostAgentsBody.replaceChildren(fragment);
    }
    if (result.truncated) {
      ui.hostStatus.textContent = "扫描已截断：达到目录、候选或时间上限。减少显式根目录后重试。";
    } else {
      ui.hostStatus.textContent = `扫描完成于 ${formatClock(new Date())}；结果仅包含元数据，不读取凭据文件。`;
    }
  }

  function renderLocalSources() {
    if (app.localSources.length === 0) {
      renderEmpty(ui.localModelsBody, 3, "未发现本地模型来源。");
    } else {
      const fragment = document.createDocumentFragment();
      for (const sourceValue of app.localSources) {
        const source = isObject(sourceValue) ? sourceValue : {};
        const models = Array.isArray(source.models) ? source.models : [];
        const row = document.createElement("tr");
        row.append(
          stackCell(localSourceLabel(source.kind), source.base_url || "—"),
          statusCell(
            source.reachable ? "reachable" : "unreachable",
            source.error || "",
          ),
          stackCell(
            models.length > 0 ? `${models.length} 个模型` : "未发现模型",
            models.join(" · ") || "—",
          ),
        );
        fragment.append(row);
      }
      ui.localModelsBody.replaceChildren(fragment);
    }
    ui.hostStatus.textContent = `本地模型发现完成于 ${formatClock(new Date())}；仅检查 loopback 服务。`;
  }

  function localSourceLabel(kind) {
    return String(kind || "") === "lmstudio" ? "LM Studio" : "Ollama";
  }

  function statusCell(status, reason) {
    const cell = document.createElement("td");
    const value = String(status || "unknown");
    const node = badge(value, !["config_found", "detected", "reachable"].includes(value));
    cell.append(node);
    if (reason) {
      const meta = document.createElement("span");
      meta.className = "cell-meta";
      meta.textContent = String(reason);
      cell.append(document.createElement("br"), meta);
    }
    return cell;
  }

  async function handleProviderCreate(event) {
    event.preventDefault();
    hideError();
    hideNotice();

    const slug = ui.providerSlug.value.trim();
    const name = ui.providerName.value.trim();
    const baseUrl = ui.providerBaseUrl.value.trim();
    const kind = ui.providerKind.value;
    const authType = ui.providerAuthType.value;
    const apiKey = ui.providerApiKey.value.trim();

    if (!slug || !name || !baseUrl) {
      showError(
        new ApiError(
          "provider_form_invalid",
          "请填写 Slug、名称和 Base URL。",
        ),
      );
      return;
    }

    const body = {
      kind,
      slug,
      name,
      base_url: baseUrl,
      auth_type: authType,
      tls_insecure: ui.providerTlsInsecure.checked,
      enabled: true,
    };
    if (apiKey) {
      body.api_key = apiKey;
    }

    setButtonBusy(ui.providerSubmit, true, "添加中…");
    try {
      await api("/admin/providers", { method: "POST", body });
      ui.providerForm.reset();
      ui.providerKind.value = "openai_compatible";
      ui.providerAuthType.value = "bearer";
      ui.providerTlsInsecure.checked = false;
      setProviderActionStatus(`已添加服务商“${name}”。`);
      await refreshAfterMutation({ providers: true });
      showNotice(`服务商“${name}”已添加。`);
    } catch (error) {
      showError(error);
    } finally {
      setButtonBusy(ui.providerSubmit, false);
    }
  }

  async function testProvider(provider) {
    requireId(provider.id, "服务商");
    setProviderActionStatus(`正在测试“${provider.name}”的 /models…`);
    clearProviderResult();
    const result = await api(
      `/admin/providers/${encodeURIComponent(provider.id)}/test`,
      {
        method: "POST",
        body: { kind: "models" },
      },
    );
    setProviderResult(`/models 测试完成 · ${provider.name}`, result);
    await refreshAfterMutation({ providers: true });
    showNotice(`“${provider.name}”的 /models 测试完成。`);
  }

  async function discoverProvider(provider) {
    requireId(provider.id, "服务商");
    setProviderActionStatus(`正在发现“${provider.name}”的上游模型…`);
    clearProviderResult();
    const result = await api(
      `/admin/providers/${encodeURIComponent(provider.id)}/discover`,
      { method: "POST" },
    );
    setProviderResult(`模型发现完成 · ${provider.name}`, result);
    await refreshAfterMutation({ providers: true, models: true });
    showNotice(`“${provider.name}”的模型发现已完成。`);
  }

  async function toggleProvider(provider) {
    requireId(provider.id, "服务商");
    const nextEnabled = provider.enabled !== true;
    const body = { enabled: nextEnabled };
    if (provider.revision !== null) {
      body.revision = provider.revision;
    }
    setProviderActionStatus(
      `${nextEnabled ? "启用" : "停用"}服务商“${provider.name}”…`,
    );
    await api(`/admin/providers/${encodeURIComponent(provider.id)}`, {
      method: "PATCH",
      body,
    });
    setProviderActionStatus(
      `服务商“${provider.name}”已${nextEnabled ? "启用" : "停用"}。`,
    );
    await refreshAfterMutation({ providers: true, models: true });
    showNotice(`服务商“${provider.name}”已更新。`);
  }

  async function deleteProvider(provider) {
    requireId(provider.id, "服务商");
    const confirmed = window.confirm(
      `确定删除服务商“${provider.name}”吗？其模型将不再参与路由。`,
    );
    if (!confirmed) {
      return;
    }
    setProviderActionStatus(`正在删除服务商“${provider.name}”…`);
    await api(`/admin/providers/${encodeURIComponent(provider.id)}`, {
      method: "DELETE",
    });
    setProviderActionStatus(`服务商“${provider.name}”已删除。`);
    clearProviderResult();
    await refreshAfterMutation({ providers: true, models: true });
    showNotice(`服务商“${provider.name}”已删除。`);
  }

  function setProviderActionStatus(message) {
    ui.providerActionStatus.textContent = message;
  }

  function setProviderResult(message, value) {
    setProviderActionStatus(message);
    const text = formatResult(value);
    if (!text) {
      clearProviderResult();
      return;
    }
    ui.providerResult.textContent = text;
    ui.providerResult.hidden = false;
  }

  function clearProviderResult() {
    ui.providerResult.textContent = "";
    ui.providerResult.hidden = true;
  }

  function renderModels() {
    if (app.models.length === 0) {
      renderEmpty(
        ui.modelsBody,
        6,
        "暂无模型。添加服务商后执行“发现模型”，或通过管理 API 手工创建。",
      );
      return;
    }

    const fragment = document.createDocumentFragment();
    for (const modelValue of app.models) {
      const model = modelView(modelValue);
      const row = document.createElement("tr");

      const idCell = document.createElement("td");
      const idStack = document.createElement("div");
      idStack.className = "cell-stack";
      const modelId = document.createElement("code");
      modelId.className = "model-id";
      modelId.textContent = model.publicId;
      idStack.append(modelId);
      if (model.source) {
        const source = document.createElement("span");
        source.className = "cell-meta";
        source.textContent = `来源: ${model.source}`;
        idStack.append(source);
      }
      idCell.append(idStack);

      const providerCell = document.createElement("td");
      providerCell.textContent = model.provider || "—";

      const enabledCell = document.createElement("td");
      enabledCell.append(
        toggleNode(
          model.enabled,
          `启用模型 ${model.publicId}`,
          (input) => patchModelField(model, "enabled", input),
          !model.id,
        ),
      );

      const staleCell = document.createElement("td");
      staleCell.append(
        model.stale ? badge("上游已移除", true) : badge("正常"),
      );

      const toolsCell = document.createElement("td");
      toolsCell.append(
        toggleNode(
          model.tools,
          `标记模型 ${model.publicId} 支持 Tools`,
          (input) => patchModelField(model, "tools", input),
          !model.id,
        ),
      );

      const visionCell = document.createElement("td");
      visionCell.append(
        toggleNode(
          model.vision,
          `标记模型 ${model.publicId} 支持 Vision`,
          (input) => patchModelField(model, "vision", input),
          !model.id,
        ),
      );

      row.append(
        idCell,
        providerCell,
        enabledCell,
        staleCell,
        toolsCell,
        visionCell,
      );
      fragment.append(row);
    }

    ui.modelsBody.replaceChildren(fragment);
  }

  function renderRoutingModelPicker() {
    if (app.models.length === 0) {
      renderEmpty(
        ui.routingModelPicker,
        6,
        "暂无可选模型。请先加载模型目录。",
      );
      return;
    }

    const models = app.models
      .map(modelView)
      .sort((left, right) => left.publicId.localeCompare(right.publicId));
    const fragment = document.createDocumentFragment();

    for (const model of models) {
      const row = document.createElement("tr");
      row.dataset.modelId = model.id;

      const selectCell = document.createElement("td");
      const select = document.createElement("input");
      select.type = "checkbox";
      select.className = "toggle routing-member-select";
      select.disabled = !model.id;
      select.setAttribute(
        "aria-label",
        `将模型 ${model.publicId} 加入路由组`,
      );
      selectCell.append(select);

      const modelCell = document.createElement("td");
      const modelStack = document.createElement("div");
      modelStack.className = "cell-stack";
      const publicID = document.createElement("code");
      publicID.className = "model-id";
      publicID.textContent = model.publicId;
      modelStack.append(publicID);
      const modelMeta = document.createElement("span");
      modelMeta.className = "cell-meta";
      const states = [];
      if (!model.enabled) {
        states.push("模型停用");
      }
      if (model.stale) {
        states.push("上游已移除");
      }
      modelMeta.textContent = states.length > 0
        ? states.join(" · ")
        : model.id || "缺少内部 ID";
      modelStack.append(modelMeta);
      modelCell.append(modelStack);

      const providerCell = textCell(model.provider || "—");
      const priorityInput = numberInput("routing-priority", "0", 0, 1);
      const weightInput = numberInput("routing-weight", "1", 1, 1);
      const memberEnabled = document.createElement("input");
      memberEnabled.type = "checkbox";
      memberEnabled.className = "toggle routing-member-enabled";
      memberEnabled.checked = true;
      memberEnabled.setAttribute(
        "aria-label",
        `启用模型 ${model.publicId} 的组成员`,
      );

      const dependentInputs = [priorityInput, weightInput, memberEnabled];
      dependentInputs.forEach((input) => {
        input.disabled = true;
      });
      select.addEventListener("change", () => {
        dependentInputs.forEach((input) => {
          input.disabled = !select.checked;
        });
      });

      const priorityCell = document.createElement("td");
      priorityCell.append(priorityInput);
      const weightCell = document.createElement("td");
      weightCell.append(weightInput);
      const memberEnabledCell = document.createElement("td");
      memberEnabledCell.append(memberEnabled);

      row.append(
        selectCell,
        modelCell,
        providerCell,
        priorityCell,
        weightCell,
        memberEnabledCell,
      );
      fragment.append(row);
    }

    ui.routingModelPicker.replaceChildren(fragment);
  }

  async function handleRoutingGroupCreate(event) {
    event.preventDefault();
    hideError();
    hideNotice();

    const name = ui.routingGroupName.value.trim();
    const description = ui.routingGroupDescription.value.trim();
    if (!/^[a-z0-9][a-z0-9_-]{0,62}$/.test(name)) {
      showError(
        new ApiError(
          "routing_group_name_invalid",
          "路由组名称须为小写字母、数字、下划线或短横线，且以小写字母或数字开头。",
        ),
      );
      return;
    }

    let members;
    try {
      members = collectRoutingMembers();
    } catch (error) {
      showError(error);
      return;
    }
    if (members.length === 0) {
      showError(
        new ApiError(
          "routing_group_members_required",
          "请至少选择一个路由组成员。",
        ),
      );
      return;
    }

    const body = {
      name,
      description,
      enabled: ui.routingGroupEnabled.value === "true",
      members,
    };

    setButtonBusy(ui.routingGroupSubmit, true, "创建中…");
    try {
      await api("/admin/routing-groups", { method: "POST", body });
      ui.routingGroupForm.reset();
      ui.routingGroupEnabled.value = "true";
      renderRoutingModelPicker();
      await refreshAfterMutation({ routingGroups: true });
      showNotice(`路由组“${name}”已创建。`);
    } catch (error) {
      showError(error);
    } finally {
      setButtonBusy(ui.routingGroupSubmit, false);
    }
  }

  function collectRoutingMembers() {
    const rows = Array.from(
      ui.routingModelPicker.querySelectorAll("tr[data-model-id]"),
    );
    const members = [];
    const seen = new Set();

    for (const row of rows) {
      const select = row.querySelector(".routing-member-select");
      if (!select || !select.checked) {
        continue;
      }
      const modelID = row.dataset.modelId || "";
      if (!modelID || seen.has(modelID)) {
        throw new ApiError(
          "routing_group_member_invalid",
          "路由组成员缺少有效的内部模型 ID。",
        );
      }
      const priority = readIntegerInput(
        row.querySelector(".routing-priority"),
        0,
      );
      const weight = readIntegerInput(
        row.querySelector(".routing-weight"),
        1,
      );
      if (priority === null || priority < 0) {
        throw new ApiError(
          "routing_group_priority_invalid",
          "Priority 必须为非负整数。",
        );
      }
      if (weight === null || weight < 1) {
        throw new ApiError(
          "routing_group_weight_invalid",
          "Weight 必须为正整数。",
        );
      }
      const enabled = row.querySelector(".routing-member-enabled");
      seen.add(modelID);
      members.push({
        model_id: modelID,
        priority,
        weight,
        enabled: Boolean(enabled && enabled.checked),
      });
    }
    return members;
  }

  function renderRoutingGroups() {
    if (app.routingGroups.length === 0) {
      renderEmpty(
        ui.routingGroupsBody,
        5,
        "暂无路由组。使用上方表单选择模型并创建。",
      );
      return;
    }

    const groups = app.routingGroups
      .map(routingGroupView)
      .sort((left, right) => left.name.localeCompare(right.name));
    const fragment = document.createDocumentFragment();

    for (const group of groups) {
      const row = document.createElement("tr");

      const nameCell = document.createElement("td");
      const nameStack = document.createElement("div");
      nameStack.className = "cell-stack";
      const name = document.createElement("strong");
      name.className = "cell-title";
      name.textContent = group.name;
      nameStack.append(name);
      const nameMeta = document.createElement("span");
      nameMeta.className = "cell-meta";
      nameMeta.textContent = group.members.length > 0
        ? `${group.members.length} 个成员`
        : "无成员";
      nameStack.append(nameMeta);
      nameCell.append(nameStack);

      const descriptionCell = document.createElement("td");
      descriptionCell.className = "group-description";
      descriptionCell.textContent = group.description || "—";

      const statusCell = document.createElement("td");
      statusCell.append(
        group.enabled ? badge("启用") : badge("停用", true),
      );

      const membersCell = document.createElement("td");
      const memberList = document.createElement("div");
      memberList.className = "member-list";
      if (group.members.length === 0) {
        memberList.textContent = "—";
      } else {
        for (const member of group.members) {
          const item = document.createElement("div");
          item.className = "member-item";
          const publicID = document.createElement("span");
          publicID.textContent = modelPublicLabel(member.modelID);
          const meta = document.createElement("span");
          meta.className = "member-meta";
          meta.textContent = `P${member.priority} · W${member.weight}${
            member.enabled ? "" : " · 停用"
          }`;
          item.append(publicID, meta);
          memberList.append(item);
        }
      }
      membersCell.append(memberList);

      const actionsCell = document.createElement("td");
      const actions = document.createElement("div");
      actions.className = "row-actions row-actions-compact";

      const toggleButton = rowButton(
        group.enabled ? "停用" : "启用",
        "button-quiet",
      );
      toggleButton.addEventListener("click", () => {
        void runAction(toggleButton, async () => {
          await toggleRoutingGroup(group);
        }, "更新中…");
      });

      const deleteButton = rowButton("删除", "button-danger");
      deleteButton.addEventListener("click", () => {
        void runAction(deleteButton, async () => {
          await deleteRoutingGroup(group);
        }, "删除中…");
      });

      actions.append(toggleButton, deleteButton);
      actionsCell.append(actions);
      row.append(
        nameCell,
        descriptionCell,
        statusCell,
        membersCell,
        actionsCell,
      );
      fragment.append(row);
    }

    ui.routingGroupsBody.replaceChildren(fragment);
  }

  async function toggleRoutingGroup(group) {
    const nextEnabled = !group.enabled;
    await api(
      `/admin/routing-groups/${encodeURIComponent(group.name)}`,
      {
        method: "PATCH",
        body: routingGroupBody(group, nextEnabled),
      },
    );
    await refreshAfterMutation({ routingGroups: true });
    showNotice(
      `路由组“${group.name}”已${nextEnabled ? "启用" : "停用"}。`,
    );
  }

  async function deleteRoutingGroup(group) {
    const confirmed = window.confirm(
      `确定删除路由组“${group.name}”吗？成员配置也会删除。`,
    );
    if (!confirmed) {
      return;
    }
    await api(
      `/admin/routing-groups/${encodeURIComponent(group.name)}`,
      { method: "DELETE" },
    );
    await refreshAfterMutation({ routingGroups: true });
    showNotice(`路由组“${group.name}”已删除。`);
  }

  function routingGroupView(value) {
    const raw = isObject(value) ? value : {};
    const members = Array.isArray(raw.members) ? raw.members : [];
    return {
      name: firstText(raw.name, "未命名路由组"),
      description: firstText(raw.description),
      enabled: readBoolean(raw.enabled) === true,
      members: members.map((value) => {
        const member = isObject(value) ? value : {};
        return {
          modelID: firstText(member.model_id, member.modelId),
          priority: firstNumber(member.priority) ?? 0,
          weight: firstNumber(member.weight) ?? 1,
          enabled: readBoolean(member.enabled) !== false,
        };
      }),
    };
  }

  function routingGroupBody(group, enabled) {
    return {
      name: group.name,
      description: group.description,
      enabled,
      members: group.members.map((member) => ({
        model_id: member.modelID,
        priority: member.priority,
        weight: member.weight,
        enabled: member.enabled,
      })),
    };
  }

  function modelPublicLabel(modelID) {
    if (!modelID) {
      return "—";
    }
    const match = app.models.find((value) => {
      const raw = isObject(value) ? value : {};
      return firstText(raw.id, raw.model_id, raw.modelId) === modelID;
    });
    return match ? modelView(match).publicId : modelID;
  }

  async function patchModelField(model, field, input) {
    if (!model.id) {
      input.checked = !input.checked;
      showError(
        new ApiError("model_id_missing", "模型响应缺少内部 ID，无法更新。"),
      );
      return;
    }

    hideError();
    const row = input.closest("tr");
    const toggles = row ? Array.from(row.querySelectorAll(".toggle")) : [input];
    const nextChecked = input.checked;
    toggles.forEach((node) => {
      node.disabled = true;
    });

    try {
      await api(`/admin/models/${encodeURIComponent(model.id)}`, {
        method: "PATCH",
        body: { [field]: nextChecked },
      });
      await refreshAfterMutation({ models: true });
      showNotice(`模型“${model.publicId}”的 ${modelFieldLabel(field)} 已更新。`);
    } catch (error) {
      input.checked = !nextChecked;
      showError(error);
    } finally {
      toggles.forEach((node) => {
        if (node.isConnected) {
          node.disabled = false;
        }
      });
    }
  }

  async function handleClientKeyCreate(event) {
    event.preventDefault();
    hideError();
    hideNotice();

    const name = ui.clientKeyName.value.trim();
    if (!name) {
      showError(
        new ApiError("client_key_name_required", "请输入客户端 Key 名称。"),
      );
      return;
    }

    let rpm;
    let dailyRequests;
    let dailyTokens;
    try {
      rpm = readLimitInput(ui.clientKeyRpm, "RPM");
      dailyRequests = readLimitInput(
        ui.clientKeyDailyRequests,
        "每日请求",
      );
      dailyTokens = readLimitInput(ui.clientKeyDailyTokens, "每日 Token");
    } catch (error) {
      showError(error);
      return;
    }
    const modelAllowlist = parseModelAllowlist(ui.clientKeyAllowlist.value);

    setButtonBusy(ui.clientKeySubmit, true, "创建中…");
    try {
      const result = await api("/admin/client-keys", {
        method: "POST",
        body: {
          name,
          rpm,
          daily_requests: dailyRequests,
          daily_tokens: dailyTokens,
          model_allowlist: modelAllowlist,
        },
      });
      ui.clientKeyForm.reset();

      const plaintextKey = extractPlaintextKey(result);
      if (plaintextKey) {
        showCreatedKey(plaintextKey);
      }

      await refreshAfterMutation({ keys: true });
      if (plaintextKey) {
        showNotice(`客户端 Key“${name}”已创建，明文只显示这一次。`);
      } else {
        showNotice("客户端 Key 已创建；响应未包含一次性明文。");
      }
    } catch (error) {
      showError(error);
    } finally {
      setButtonBusy(ui.clientKeySubmit, false);
    }
  }

  function renderClientKeys() {
    if (app.clientKeys.length === 0) {
      renderEmpty(
        ui.clientKeysBody,
        6,
        "暂无客户端 Key。使用上方表单创建一个新的调用凭据。",
      );
      return;
    }

    const fragment = document.createDocumentFragment();
    for (const keyValue of app.clientKeys) {
      const key = clientKeyView(keyValue);
      const row = document.createElement("tr");

      const nameCell = document.createElement("td");
      const nameStack = document.createElement("div");
      nameStack.className = "cell-stack";
      const name = document.createElement("strong");
      name.className = "cell-title";
      name.textContent = key.name;
      nameStack.append(name);
      if (key.id) {
        const id = document.createElement("span");
        id.className = "cell-meta";
        id.textContent = key.id;
        nameStack.append(id);
      }
      nameCell.append(nameStack);

      const prefixCell = document.createElement("td");
      const prefix = document.createElement("code");
      prefix.className = "key-prefix";
      prefix.textContent = key.prefix || "—";
      prefixCell.append(prefix);

      const statusCell = document.createElement("td");
      const statusBadges = document.createElement("div");
      statusBadges.className = "badge-stack";
      if (key.revoked) {
        statusBadges.append(badge("已撤销", true));
      } else if (key.enabled === true) {
        statusBadges.append(badge("启用"));
      } else if (key.enabled === false) {
        statusBadges.append(badge("停用", true));
      } else {
        statusBadges.append(badge("状态未知", true));
      }
      statusCell.append(statusBadges);

      const usedCell = document.createElement("td");
      usedCell.textContent = formatDate(key.lastUsedAt);

      const limitsCell = document.createElement("td");
      limitsCell.className = "limits-cell";
      limitsCell.textContent = formatLimits(key.limits);

      const actionsCell = document.createElement("td");
      if (key.revoked) {
        const unavailable = document.createElement("span");
        unavailable.className = "cell-meta";
        unavailable.textContent = "已撤销";
        actionsCell.append(unavailable);
      } else {
        const actions = document.createElement("div");
        actions.className = "row-actions row-actions-compact";

        const toggleButton = rowButton(
          key.enabled === true ? "停用" : "启用",
          "button-quiet",
        );
        toggleButton.addEventListener("click", () => {
          void runAction(toggleButton, async () => {
            await toggleClientKey(key);
          }, "更新中…");
        });

        const rotateButton = rowButton("轮换", "button-quiet");
        rotateButton.addEventListener("click", () => {
          void runAction(rotateButton, async () => {
            await rotateClientKey(key);
          }, "轮换中…");
        });

        const revokeButton = rowButton("撤销", "button-danger");
        revokeButton.addEventListener("click", () => {
          void runAction(revokeButton, async () => {
            await revokeClientKey(key);
          }, "撤销中…");
        });

        actions.append(toggleButton, rotateButton, revokeButton);
        actionsCell.append(actions);
      }

      row.append(
        nameCell,
        prefixCell,
        statusCell,
        usedCell,
        limitsCell,
        actionsCell,
      );
      fragment.append(row);
    }

    ui.clientKeysBody.replaceChildren(fragment);
  }

  async function toggleClientKey(key) {
    requireId(key.id, "客户端 Key");
    const nextEnabled = key.enabled !== true;
    await api(`/admin/client-keys/${encodeURIComponent(key.id)}`, {
      method: "PATCH",
      body: { enabled: nextEnabled },
    });
    await refreshAfterMutation({ keys: true });
    showNotice(
      `客户端 Key“${key.name}”已${nextEnabled ? "启用" : "停用"}。`,
    );
  }

  async function revokeClientKey(key) {
    requireId(key.id, "客户端 Key");
    const confirmed = window.confirm(
      `确定撤销客户端 Key“${key.name}”吗？撤销后不可恢复。`,
    );
    if (!confirmed) {
      return;
    }
    await api(`/admin/client-keys/${encodeURIComponent(key.id)}`, {
      method: "DELETE",
    });
    await refreshAfterMutation({ keys: true });
    showNotice(`客户端 Key“${key.name}”已撤销。`);
  }

  async function rotateClientKey(key) {
    requireId(key.id, "客户端 Key");
    const confirmed = window.confirm(
      `确定轮换客户端 Key“${key.name}”吗？旧 Key 将立即失效。`,
    );
    if (!confirmed) {
      return;
    }
    const result = await api(
      `/admin/client-keys/${encodeURIComponent(key.id)}/rotate`,
      { method: "POST" },
    );
    const plaintextKey = extractPlaintextKey(result);
    if (plaintextKey) {
      showCreatedKey(plaintextKey);
    }
    await refreshAfterMutation({ keys: true });
    if (plaintextKey) {
      showNotice(`客户端 Key“${key.name}”已轮换，明文只显示这一次。`);
    } else {
      showNotice("客户端 Key 已轮换；响应未包含一次性明文。");
    }
  }

  async function refreshAfterMutation(options = {}) {
    const jobs = [
      loadState().catch((error) => {
        if (error.status !== 401) {
          showError(error);
        }
      }),
    ];
    if (options.providers) {
      jobs.push(loadProviders());
    }
    if (options.models) {
      jobs.push(loadModels());
    }
    if (options.keys) {
      jobs.push(loadClientKeys());
    }
    if (options.routingGroups) {
      jobs.push(loadRoutingGroups());
    }
    await Promise.allSettled(jobs);
  }

  async function api(path, options = {}) {
    const method = String(options.method || "GET").toUpperCase();
    if (!["GET", "HEAD"].includes(method)) {
      await ensureCsrf();
    }
    return request(path, {
      method,
      body: options.body,
    });
  }

  async function request(path, options = {}) {
    const method = String(options.method || "GET").toUpperCase();
    const unsafe = !["GET", "HEAD"].includes(method);
    const headers = {
      Accept: "application/json",
    };
    let body;

    if (unsafe && app.csrf) {
      headers["X-CSRF-Token"] = app.csrf;
    }
    if (options.body !== undefined && options.body !== null) {
      headers["Content-Type"] = "application/json";
      body = JSON.stringify(options.body);
    }

    let response;
    try {
      response = await fetch(path, {
        method,
        credentials: "same-origin",
        headers,
        body,
        cache: "no-store",
        referrerPolicy: "no-referrer",
      });
    } catch {
      throw new ApiError(
        "network_error",
        "无法连接管理服务，请确认服务正在运行。",
      );
    }

    const raw = await response.text();
    let data = null;
    if (raw) {
      try {
        data = JSON.parse(raw);
      } catch {
        data = raw;
      }
    }

    if (!response.ok) {
      const error = apiErrorFromResponse(data, response.status);
      if (response.status === 401 && path !== "/admin/session") {
        expireSession();
      }
      throw error;
    }

    return data;
  }

  function apiErrorFromResponse(data, status) {
    let code = "";
    let message = "";
    const nested = isObject(data) ? data.error : null;

    if (isObject(nested)) {
      if (typeof nested.code === "string") {
        code = nested.code;
      }
      if (typeof nested.message === "string") {
        message = nested.message;
      }
    } else if (typeof nested === "string") {
      code = nested;
    }

    if (!code && isObject(data) && typeof data.code === "string") {
      code = data.code;
    }
    if (!message && isObject(data) && typeof data.message === "string") {
      message = data.message;
    }
    if (!code) {
      code = `http_${status}`;
    }
    if (!message) {
      message = code;
    }
    return new ApiError(code, message, status);
  }

  async function ensureCsrf() {
    if (app.csrf) {
      return app.csrf;
    }

    const payload = await request("/admin/state");
    rememberCsrf(payload);
    if (!app.csrf) {
      throw new ApiError(
        "admin_csrf_missing",
        "管理服务未返回 CSRF Token，请刷新页面后重新登录。",
      );
    }
    return app.csrf;
  }

  function expireSession() {
    if (ui.consoleView && !ui.consoleView.hidden) {
      showLogin(false, true);
    }
  }

  function rememberCsrf(payload) {
    const candidates = [
      payload,
      payload?.data,
      payload?.session,
      payload?.result,
    ];
    for (const candidate of candidates) {
      if (!isObject(candidate)) {
        continue;
      }
      const value = firstText(
        candidate.csrf_token,
        candidate.csrfToken,
        candidate.csrf,
      );
      if (value) {
        app.csrf = value;
        return;
      }
    }
  }

  function stateObject(payload) {
    if (!isObject(payload)) {
      return {};
    }
    if (
      "ready" in payload ||
      "generation" in payload ||
      "repair_required" in payload ||
      "providers" in payload ||
      "models" in payload
    ) {
      return payload;
    }
    if (isObject(payload.state)) {
      return payload.state;
    }
    if (isObject(payload.data)) {
      return payload.data;
    }
    return payload;
  }

  function inlineState(payload) {
    if (!isObject(payload)) {
      return null;
    }
    if (
      "ready" in payload ||
      "generation" in payload ||
      "repair_required" in payload
    ) {
      return payload;
    }
    if (isObject(payload.state)) {
      return payload.state;
    }
    if (isObject(payload.data?.state)) {
      return payload.data.state;
    }
    return null;
  }

  function providerView(value) {
    const raw = isObject(value) ? value : {};
    const id = firstText(raw.id, raw.provider_id, raw.providerId);
    const slug = firstText(raw.slug, raw.provider_slug);
    return {
      raw,
      id,
      slug,
      name: firstText(raw.name, raw.display_name, raw.label, slug, id, "未命名服务商"),
      baseUrl: firstText(raw.base_url, raw.baseUrl, raw.url),
      kind: firstText(raw.kind, raw.provider_kind),
      authType: firstText(raw.auth_type, raw.authType, raw.auth),
      tlsInsecure: readBoolean(raw.tls_insecure) === true,
      enabled: readBoolean(raw.enabled),
      unreadable:
        readBoolean(
          firstDefined(
            raw.credential_unreadable,
            raw.unreadable,
            raw.credential_error,
          ),
        ) === true,
      revision: firstNumber(raw.revision),
    };
  }

  function modelView(value) {
    const raw = isObject(value) ? value : {};
    const capabilities = isObject(raw.capabilities)
      ? raw.capabilities
      : isObject(raw.caps)
        ? raw.caps
        : {};
    const provider = raw.provider;
    let providerLabel = "";
    if (typeof provider === "string") {
      providerLabel = providerLabelForID(provider) || provider;
    } else if (isObject(provider)) {
      providerLabel = firstText(
        provider.slug,
        provider.name,
        provider.id,
      );
    }
    if (!providerLabel) {
      providerLabel = firstText(raw.provider_slug, raw.provider_name);
    }
    if (!providerLabel) {
      providerLabel = providerLabelForID(
        firstText(raw.provider_id, raw.providerId),
      );
    }

    return {
      raw,
      id: firstText(raw.id, raw.model_id, raw.modelId),
      publicId: firstText(
        raw.public_id,
        raw.publicId,
        raw.upstream_id,
        raw.upstreamId,
        raw.id,
        "—",
      ),
      provider: providerLabel,
      enabled: readBoolean(raw.enabled) === true,
      stale: readBoolean(firstDefined(raw.stale, raw.is_stale)) === true,
      tools:
        readBoolean(firstDefined(raw.tools, capabilities.tools)) === true,
      vision:
        readBoolean(firstDefined(raw.vision, capabilities.vision)) === true,
      source: firstText(raw.source),
    };
  }

  function clientKeyView(value) {
    const raw = isObject(value) ? value : {};
    const rawLimits = firstDefined(
      raw.limits_summary,
      raw.limitsSummary,
      raw.limits,
      raw.limit,
      raw.quota,
      raw.rate_limits,
      raw.rate_limit,
      raw.constraints,
    );
    const nestedLimits = isObject(rawLimits) ? rawLimits : {};
    const limits = {
      rpm: firstNumber(raw.rpm, nestedLimits.rpm) ?? 0,
      daily_requests:
        firstNumber(
          raw.daily_requests,
          raw.dailyRequests,
          nestedLimits.daily_requests,
          nestedLimits.dailyRequests,
        ) ?? 0,
      daily_tokens:
        firstNumber(
          raw.daily_tokens,
          raw.dailyTokens,
          nestedLimits.daily_tokens,
          nestedLimits.dailyTokens,
        ) ?? 0,
      model_allowlist: parseModelAllowlist(
        firstDefined(
          raw.model_allowlist,
          raw.modelAllowlist,
          nestedLimits.model_allowlist,
          nestedLimits.modelAllowlist,
          nestedLimits.allowed_models,
        ),
      ),
    };
    return {
      raw,
      id: firstText(raw.id, raw.key_id, raw.client_key_id),
      name: firstText(raw.name, raw.label, "未命名 Key"),
      prefix: firstText(raw.prefix, raw.key_prefix),
      enabled: readBoolean(raw.enabled),
      revoked:
        readBoolean(firstDefined(raw.revoked, raw.is_revoked)) === true ||
        !isMissing(firstDefined(raw.revoked_at, raw.revokedAt)),
      lastUsedAt: firstDefined(
        raw.last_used_at,
        raw.lastUsedAt,
        raw.last_used,
      ),
      limits,
    };
  }

  function extractList(payload, preferredKeys) {
    const keys = [...preferredKeys, "items", "results", "rows", "data"];

    function search(value, depth) {
      if (Array.isArray(value)) {
        return value;
      }
      if (!isObject(value) || depth > 2) {
        return null;
      }
      for (const key of keys) {
        if (Array.isArray(value[key])) {
          return value[key];
        }
      }
      for (const container of ["data", "result", "payload"]) {
        if (isObject(value[container])) {
          const found = search(value[container], depth + 1);
          if (found) {
            return found;
          }
        }
      }
      return null;
    }

    return search(payload, 0) || [];
  }

  function extractPlaintextKey(payload) {
    const candidates = [
      payload?.plaintext_key,
      payload?.data?.plaintext_key,
      payload?.result?.plaintext_key,
      payload?.key?.plaintext_key,
      payload?.data?.key?.plaintext_key,
    ];
    for (const candidate of candidates) {
      const value = typeof candidate === "string" ? candidate.trim() : "";
      if (value) {
        return value;
      }
    }
    return "";
  }

  function countText(value) {
    const count = firstNumber(value);
    return count === null
      ? "—"
      : new Intl.NumberFormat("zh-CN", {
          maximumFractionDigits: 0,
        }).format(count);
  }

  function textCell(text) {
    const cell = document.createElement("td");
    cell.textContent = isMissing(text) ? "—" : String(text);
    return cell;
  }

  function codeCell(text) {
    const cell = document.createElement("td");
    const code = document.createElement("code");
    code.className = "model-id";
    code.textContent = isMissing(text) ? "—" : String(text);
    cell.append(code);
    return cell;
  }

  function numberCell(value) {
    const cell = document.createElement("td");
    cell.textContent = countText(value);
    return cell;
  }

  function stackCell(title, meta) {
    const cell = document.createElement("td");
    const stack = document.createElement("div");
    stack.className = "cell-stack";
    const titleNode = document.createElement("strong");
    titleNode.className = "cell-title";
    titleNode.textContent = isMissing(title) ? "—" : String(title);
    stack.append(titleNode);
    if (!isMissing(meta)) {
      const metaNode = document.createElement("span");
      metaNode.className = "cell-meta";
      metaNode.textContent = String(meta);
      stack.append(metaNode);
    }
    cell.append(stack);
    return cell;
  }

  function pad2(value) {
    return String(Math.max(0, Number(value) || 0)).padStart(2, "0");
  }

  function parseTimeMillis(value) {
    if (typeof value === "number") {
      if (!Number.isFinite(value)) {
        return Number.NaN;
      }
      return value < 1000000000000 ? value * 1000 : value;
    }
    if (typeof value !== "string" || !value.trim()) {
      return Number.NaN;
    }
    const text = value.trim();
    if (/^-?\d+(?:\.\d+)?$/.test(text)) {
      const numeric = Number(text);
      return numeric < 1000000000000 ? numeric * 1000 : numeric;
    }
    const normalized = text.replace(/(\.\d{3})\d+/, "$1");
    return Date.parse(normalized);
  }

  function clientKeyIdentity(keyID) {
    if (!keyID) {
      return { title: "—", meta: "" };
    }
    const match = app.clientKeys.find((value) => {
      const raw = isObject(value) ? value : {};
      return firstText(raw.id, raw.key_id, raw.client_key_id) === keyID;
    });
    if (!match) {
      return { title: keyID, meta: "未在 Key 列表中" };
    }
    const key = clientKeyView(match);
    return {
      title: key.name,
      meta: [key.prefix ? `前缀 ${key.prefix}` : "", key.id]
        .filter(Boolean)
        .join(" · "),
    };
  }

  function providerLabelForID(providerID) {
    if (!providerID) {
      return "";
    }
    const match = app.providers.find((value) => {
      const raw = isObject(value) ? value : {};
      return firstText(raw.id, raw.provider_id, raw.providerId) === providerID;
    });
    if (!match) {
      return providerID;
    }
    const provider = providerView(match);
    return provider.slug || provider.name || provider.id || providerID;
  }

  function providerIdentity(providerID) {
    if (!providerID) {
      return { title: "—", meta: "" };
    }
    const match = app.providers.find((value) => {
      const raw = isObject(value) ? value : {};
      return firstText(raw.id, raw.provider_id, raw.providerId) === providerID;
    });
    if (!match) {
      return { title: providerID, meta: "未在服务商列表中" };
    }
    const provider = providerView(match);
    return {
      title: provider.name || provider.slug || provider.id,
      meta: provider.slug
        ? `slug: ${provider.slug}`
        : provider.id || providerID,
    };
  }

  function numberInput(className, value, min, step) {
    const input = document.createElement("input");
    input.type = "number";
    input.className = `mini-input ${className}`.trim();
    input.value = String(value);
    input.min = String(min);
    input.step = String(step);
    input.inputMode = "numeric";
    return input;
  }

  function readIntegerInput(input, fallback) {
    if (!input) {
      return fallback;
    }
    const text = String(input.value || "").trim();
    if (!text) {
      return fallback;
    }
    if (!/^\d+$/.test(text)) {
      return null;
    }
    const value = Number(text);
    return Number.isSafeInteger(value) ? value : null;
  }

  function readLimitInput(input, label) {
    const value = readIntegerInput(input, 0);
    if (value === null) {
      throw new ApiError(
        "client_key_limit_invalid",
        `${label}必须为非负整数。`,
      );
    }
    return value;
  }

  function parseModelAllowlist(value) {
    if (Array.isArray(value)) {
      return uniqueStrings(value);
    }
    if (typeof value !== "string") {
      return [];
    }
    return uniqueStrings(value.split(/[,，\n]/));
  }

  function uniqueStrings(values) {
    const result = [];
    const seen = new Set();
    for (const value of values) {
      const text = typeof value === "string" ? value.trim() : "";
      if (!text || seen.has(text)) {
        continue;
      }
      seen.add(text);
      result.push(text);
    }
    return result;
  }

  function mergeCounts(...values) {
    const result = {
      total: null,
      enabled: null,
      stale: null,
      unreadable: null,
      revoked: null,
    };
    for (const value of values) {
      const counts = asCounts(value);
      for (const key of Object.keys(result)) {
        if (result[key] === null && counts[key] !== null) {
          result[key] = counts[key];
        }
      }
    }
    return result;
  }

  function asCounts(value) {
    const result = {
      total: null,
      enabled: null,
      stale: null,
      unreadable: null,
      revoked: null,
    };
    if (Array.isArray(value)) {
      result.total = value.length;
      return result;
    }
    if (typeof value === "number" && Number.isFinite(value)) {
      result.total = value;
      return result;
    }
    if (!isObject(value)) {
      return result;
    }
    result.total = firstNumber(
      value.total,
      value.count,
      value.all,
    );
    result.enabled = firstNumber(
      value.enabled,
      value.active,
      value.ready,
    );
    result.stale = firstNumber(value.stale, value.removed);
    result.unreadable = firstNumber(
      value.unreadable,
      value.invalid,
      value.error,
    );
    result.revoked = firstNumber(value.revoked, value.disabled);
    return result;
  }

  function oneTimeText(value) {
    if (typeof value === "string") {
      return value.trim();
    }
    if (isObject(value)) {
      for (const key of [
        "plaintext_key",
        "local_key",
        "key",
        "token",
        "secret",
        "value",
        "text",
      ]) {
        if (typeof value[key] === "string" && value[key].trim()) {
          return value[key].trim();
        }
      }
      return formatResult(value);
    }
    if (value === null || value === undefined) {
      return "";
    }
    return String(value);
  }

  function formatLimits(value) {
    if (isMissing(value) || value === "") {
      return "未设置";
    }
    if (typeof value === "string") {
      return value;
    }
    if (typeof value === "number") {
      return value === 0 ? "不限" : String(value);
    }
    if (typeof value === "boolean") {
      return String(value);
    }
    if (Array.isArray(value)) {
      return value.length > 0
        ? value.map(formatLimitValue).join("；")
        : "不限";
    }
    if (!isObject(value)) {
      return String(value);
    }

    const entries = Object.entries(value).filter(
      ([, entry]) => entry !== null && entry !== undefined && entry !== "",
    );
    if (entries.length === 0) {
      return "未设置";
    }
    return entries
      .map(([key, entry]) => `${limitLabel(key)}: ${formatLimitValue(entry)}`)
      .join(" · ");
  }

  function formatLimitValue(value) {
    if (
      (typeof value === "number" && value === 0) ||
      (typeof value === "string" && value.trim() === "0")
    ) {
      return "不限";
    }
    if (Array.isArray(value)) {
      return value.length > 0
        ? value.map(formatLimitValue).join("、")
        : "不限";
    }
    if (isObject(value)) {
      return Object.entries(value)
        .map(([key, entry]) => `${limitLabel(key)} ${formatLimitValue(entry)}`)
        .join("、");
    }
    return String(value);
  }

  function limitLabel(key) {
    const labels = {
      rpm: "每分钟",
      rpd: "每日",
      daily: "每日",
      monthly: "每月",
      requests: "请求",
      max_requests: "最大请求",
      tokens: "Token",
      max_tokens: "最大 Token",
      concurrency: "并发",
      rate: "速率",
      cost: "额度",
      budget: "预算",
      daily_requests: "每日请求",
      daily_tokens: "每日 Token",
      model_allowlist: "白名单",
      max_input_tokens: "最大输入 Token",
      max_output_tokens: "最大输出 Token",
    };
    return labels[key] || key;
  }

  function formatDate(value) {
    if (isMissing(value) || value === "" || value === 0) {
      return "从未使用";
    }
    let date;
    if (typeof value === "number") {
      date = new Date(value < 1000000000000 ? value * 1000 : value);
    } else {
      date = new Date(value);
    }
    if (Number.isNaN(date.getTime())) {
      return String(value);
    }
    return new Intl.DateTimeFormat("zh-CN", {
      year: "numeric",
      month: "2-digit",
      day: "2-digit",
      hour: "2-digit",
      minute: "2-digit",
      second: "2-digit",
      hour12: false,
    }).format(date);
  }

  function formatClock(date) {
    return new Intl.DateTimeFormat("zh-CN", {
      hour: "2-digit",
      minute: "2-digit",
      second: "2-digit",
      hour12: false,
    }).format(date);
  }

  function formatErrorValue(value) {
    if (typeof value === "string") {
      return value;
    }
    if (Array.isArray(value)) {
      return value.map(formatErrorValue).join("\n");
    }
    if (isObject(value)) {
      if (isObject(value.error)) {
        return formatErrorValue(value.error);
      }
      const code = firstText(value.code, value.error_code);
      const message = firstText(value.message, value.msg);
      if (code) {
        return `[${code}] ${message || code}`;
      }
    }
    return formatResult(value);
  }

  function formatResult(value) {
    if (isMissing(value)) {
      return "";
    }
    const clean = redactForDisplay(value);
    if (typeof clean === "string") {
      return clean;
    }
    try {
      const text = JSON.stringify(clean, null, 2);
      return text === undefined ? String(clean) : text;
    } catch {
      return String(clean);
    }
  }

  function redactForDisplay(value, depth = 0, seen = new WeakSet()) {
    if (value === null || typeof value !== "object") {
      return value;
    }
    if (depth > 8) {
      return "[内容已截断]";
    }
    if (seen.has(value)) {
      return "[循环引用]";
    }
    seen.add(value);

    if (Array.isArray(value)) {
      return value.map((entry) =>
        redactForDisplay(entry, depth + 1, seen)
      );
    }

    const output = {};
    for (const [key, entry] of Object.entries(value)) {
      if (isSensitiveKey(key)) {
        output[key] = "[已隐藏]";
      } else {
        output[key] = redactForDisplay(entry, depth + 1, seen);
      }
    }
    return output;
  }

  function isSensitiveKey(key) {
    return /(token|secret|password|authorization|api[_-]?key|plaintext[_-]?key|csrf|credential)/i.test(
      key,
    );
  }

  function renderEmpty(tbody, colspan, message) {
    const row = document.createElement("tr");
    const cell = document.createElement("td");
    cell.colSpan = colspan;
    cell.className = "empty-cell";
    cell.textContent = message;
    row.append(cell);
    tbody.replaceChildren(row);
  }

  function renderTableError(tbody, colspan, error) {
    const row = document.createElement("tr");
    const cell = document.createElement("td");
    cell.colSpan = colspan;
    cell.className = "section-error";
    cell.textContent = errorText(error);
    row.append(cell);
    tbody.replaceChildren(row);
  }

  function badge(text, muted = false) {
    const node = document.createElement("span");
    node.className = muted ? "badge badge-muted" : "badge";
    node.textContent = text;
    return node;
  }

  function rowButton(label, extraClass) {
    const button = document.createElement("button");
    button.type = "button";
    button.className = "button button-small";
    if (extraClass) {
      button.className += ` ${extraClass}`;
    }
    button.textContent = label;
    return button;
  }

  function toggleNode(checked, label, onChange, disabled) {
    const input = document.createElement("input");
    input.type = "checkbox";
    input.className = "toggle";
    input.checked = checked;
    input.disabled = disabled;
    input.setAttribute("aria-label", label);
    input.title = disabled ? "缺少可用的内部 ID" : label;
    input.addEventListener("change", () => {
      onChange(input);
    });
    return input;
  }

  function modelFieldLabel(field) {
    if (field === "enabled") {
      return "启用状态";
    }
    if (field === "tools") {
      return "Tools 能力";
    }
    if (field === "vision") {
      return "Vision 能力";
    }
    return field;
  }

  function authLabel(value) {
    if (!value) {
      return "—";
    }
    const normalized = String(value).toLowerCase();
    if (normalized === "bearer") {
      return "Bearer";
    }
    if (normalized === "none") {
      return "无认证";
    }
    return String(value);
  }

  async function runAction(button, task, busyLabel, onError) {
    if (!button || button.disabled) {
      return;
    }
    hideError();
    setButtonBusy(button, true, busyLabel);
    try {
      await task();
    } catch (error) {
      if (typeof onError === "function") {
        try {
          onError(error);
        } catch {
          // Keep the primary API error visible.
        }
      }
      showError(error);
    } finally {
      setButtonBusy(button, false);
    }
  }

  function setButtonBusy(button, busy, label) {
    if (!button) {
      return;
    }
    if (busy) {
      if (!button.dataset.idleLabel) {
        button.dataset.idleLabel = button.textContent;
      }
      button.disabled = true;
      button.setAttribute("aria-busy", "true");
      if (label) {
        button.textContent = label;
      }
      return;
    }

    button.disabled = false;
    button.removeAttribute("aria-busy");
    if (button.dataset.idleLabel) {
      button.textContent = button.dataset.idleLabel;
      delete button.dataset.idleLabel;
    }
  }

  function requireId(id, label) {
    if (!id) {
      throw new ApiError(
        "id_missing",
        `${label}响应缺少内部 ID，无法执行操作。`,
      );
    }
  }

  function showError(error) {
    hideNotice();
    ui.globalAlertText.textContent = errorText(error);
    ui.globalAlert.hidden = false;
  }

  function hideError() {
    ui.globalAlert.hidden = true;
    ui.globalAlertText.textContent = "";
  }

  function showNotice(message) {
    window.clearTimeout(app.noticeTimer);
    ui.globalNotice.textContent = message;
    ui.globalNotice.hidden = false;
    app.noticeTimer = window.setTimeout(() => {
      ui.globalNotice.hidden = true;
      ui.globalNotice.textContent = "";
    }, 8000);
  }

  function hideNotice() {
    window.clearTimeout(app.noticeTimer);
    ui.globalNotice.hidden = true;
    ui.globalNotice.textContent = "";
  }

  function showLoginError(message) {
    ui.loginError.textContent = message;
    ui.loginError.hidden = false;
  }

  function hideLoginError() {
    ui.loginError.hidden = true;
    ui.loginError.textContent = "";
  }

  function errorText(error) {
    if (error instanceof ApiError) {
      return `[${error.code}] ${error.message || error.code}`;
    }
    if (error && typeof error.message === "string" && error.message) {
      return `[ui_error] ${error.message}`;
    }
    return "[ui_error] 操作未完成。";
  }

  async function copyText(text) {
    if (!text) {
      showError(
        new ApiError("nothing_to_copy", "没有可复制的一次性文本。"),
      );
      return;
    }
    if (!navigator.clipboard || !navigator.clipboard.writeText) {
      showError(
        new ApiError(
          "clipboard_unavailable",
          "当前浏览器无法自动复制，请手动选择文本。",
        ),
      );
      return;
    }
    try {
      await navigator.clipboard.writeText(text);
      showNotice("已复制到剪贴板。");
    } catch {
      showError(
        new ApiError(
          "clipboard_unavailable",
          "复制失败，请手动选择文本。",
        ),
      );
    }
  }

  function dismissLocalKey() {
    app.localKeyText = "";
    ui.localKeyOnce.textContent = "";
    ui.localKeyWrap.hidden = true;
  }

  function dismissCreatedKey() {
    app.createdKeyText = "";
    ui.createdKeyPlaintext.textContent = "";
    ui.createdKeyWrap.hidden = true;
  }

  function showCreatedKey(text) {
    const value = String(text || "").trim();
    if (!value) {
      return false;
    }
    app.createdKeyText = value;
    ui.createdKeyPlaintext.textContent = value;
    ui.createdKeyWrap.hidden = false;
    return true;
  }

  function firstText(...values) {
    for (const value of values) {
      if (typeof value === "string" && value.trim()) {
        return value.trim();
      }
      if (typeof value === "number" && Number.isFinite(value)) {
        return String(value);
      }
      if (typeof value === "boolean") {
        return String(value);
      }
    }
    return "";
  }

  function firstNumber(...values) {
    for (const value of values) {
      if (typeof value === "number" && Number.isFinite(value)) {
        return value;
      }
      if (typeof value === "string" && value.trim() !== "") {
        const parsed = Number(value);
        if (Number.isFinite(parsed)) {
          return parsed;
        }
      }
    }
    return null;
  }

  function firstDefined(...values) {
    for (const value of values) {
      if (value !== null && value !== undefined) {
        return value;
      }
    }
    return null;
  }

  function readBoolean(value) {
    if (value === null || value === undefined || value === "") {
      return null;
    }
    if (typeof value === "boolean") {
      return value;
    }
    if (value === 1 || value === "1" || value === "true") {
      return true;
    }
    if (value === 0 || value === "0" || value === "false") {
      return false;
    }
    return Boolean(value);
  }

  function isObject(value) {
    return value !== null && typeof value === "object" && !Array.isArray(value);
  }

  function isMissing(value) {
    return value === null || value === undefined || value === "";
  }

  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", init, { once: true });
  } else {
    init();
  }
})();
