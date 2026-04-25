let historyLoadCounter = 0;
let historyPrinters = {};

function historyFetchJson(url, options) {
  return fetch(url, options).then(async (response) => {
    const data = await response.json().catch(() => ({}));
    if (!response.ok || data.error) {
      throw new Error(data.error || response.statusText || "Request failed");
    }
    return data;
  });
}

function formatHistoryTimestamp(timestamp) {
  if (!timestamp) {
    return "Unknown";
  }

  return new Date(timestamp).toLocaleString();
}

function formatHistoryFilamentUsed(value) {
  if (!Number.isFinite(value) || value <= 0) {
    return "Unknown";
  }

  return `${Math.round(value)}g`;
}

function normalizeHistoryWeightValue(value) {
  const parsed = Number(value);
  if (!Number.isFinite(parsed) || parsed <= 0) {
    return "";
  }

  return parsed.toFixed(2);
}

function parseHistoryWeightInput(input) {
  const rawValue = input?.value?.trim() || "";
  if (rawValue === "") {
    return {
      valid: true,
      value: 0,
      normalized: "",
    };
  }

  const parsed = Number(rawValue);
  if (!Number.isFinite(parsed) || parsed < 0) {
    return {
      valid: false,
      value: 0,
      normalized: rawValue,
    };
  }

  return {
    valid: true,
    value: parsed,
    normalized: normalizeHistoryWeightValue(parsed),
  };
}

function getHistorySpoolMeta(spoolID, spoolsById) {
  if (spoolID == null) {
    return {
      label: "Unknown spool",
      detail: "No spool recorded",
      colorHex: "",
    };
  }

  const spool = spoolsById.get(spoolID);
  if (!spool) {
    return {
      label: `Spool #${spoolID}`,
      detail: "No current Spoolman details",
      colorHex: "",
    };
  }

  const material = spool.material || "Unknown Material";
  const brand = spool.brand || "Unknown Brand";
  const name = spool.name || "Unnamed Spool";
  const weight =
    spool.remaining_weight != null
      ? `${Math.round(spool.remaining_weight)}g remaining`
      : "Weight unknown";

  return {
    label: `[${spool.id}] ${material} - ${brand} - ${name}`,
    detail: weight,
    colorHex: spool.filament?.color_hex || "",
  };
}

function setHistoryFeedback(message, type) {
  const feedback = document.getElementById("history-feedback");
  if (!feedback) {
    return;
  }

  if (!message) {
    feedback.textContent = "";
    feedback.className = "history-feedback hidden";
    return;
  }

  feedback.textContent = message;
  feedback.className = `history-feedback ${type}`;
}

function getHistoryImportElements() {
  return {
    printerSelect: document.getElementById("history-import-printer"),
    toolheadSelect: document.getElementById("history-import-toolhead"),
    fileInput: document.getElementById("history-import-file"),
    importButton: document.getElementById("history-import-button"),
  };
}

function renderHistoryToolheadOptions(printerId) {
  const { toolheadSelect } = getHistoryImportElements();
  if (!toolheadSelect) {
    return;
  }

  toolheadSelect.innerHTML = "";

  const printer = historyPrinters[printerId];
  if (!printer) {
    const option = document.createElement("option");
    option.value = "";
    option.textContent = "No toolheads available";
    toolheadSelect.appendChild(option);
    toolheadSelect.disabled = true;
    return;
  }

  const toolheadNames = printer.toolhead_names || {};
  const toolheadCount = printer.toolheads || 0;

  for (let toolheadId = 0; toolheadId < toolheadCount; toolheadId++) {
    const option = document.createElement("option");
    option.value = String(toolheadId);
    option.textContent = toolheadNames[toolheadId] || `Toolhead ${toolheadId}`;
    toolheadSelect.appendChild(option);
  }

  toolheadSelect.disabled = toolheadCount === 0;
}

function renderHistoryPrinterOptions(printers) {
  const { printerSelect, importButton } = getHistoryImportElements();
  if (!printerSelect) {
    return;
  }

  historyPrinters = printers || {};
  printerSelect.innerHTML = "";

  const printerEntries = Object.entries(historyPrinters).sort(
    ([, left], [, right]) => {
      return (left.name || "").localeCompare(right.name || "");
    },
  );

  if (!printerEntries.length) {
    const option = document.createElement("option");
    option.value = "";
    option.textContent = "No printers configured";
    printerSelect.appendChild(option);
    printerSelect.disabled = true;
    if (importButton) {
      importButton.disabled = true;
    }
    renderHistoryToolheadOptions("");
    return;
  }

  printerEntries.forEach(([printerId, printer]) => {
    const option = document.createElement("option");
    option.value = printerId;
    option.textContent = printer.name || printerId;
    printerSelect.appendChild(option);
  });

  printerSelect.disabled = false;
  if (importButton) {
    importButton.disabled = false;
  }
  renderHistoryToolheadOptions(printerSelect.value);
}

function loadHistoryImportOptions() {
  return historyFetchJson("/api/printers")
    .then((data) => {
      renderHistoryPrinterOptions(data.printers || {});
    })
    .catch(() => {
      renderHistoryPrinterOptions({});
      return null;
    });
}

async function readHistoryImportPayload() {
  const { fileInput } = getHistoryImportElements();
  if (fileInput && fileInput.files && fileInput.files[0]) {
    return fileInput.files[0].text();
  }

  return "";
}

function formatHistoryImportSummary(summary) {
  if (!summary) {
    return "Print history imported.";
  }

  const messageParts = [];
  messageParts.push(`Imported ${summary.imported_rows || 0} row(s)`);
  messageParts.push(`from ${summary.jobs_seen || 0} job(s)`);

  if (summary.duplicate_rows) {
    messageParts.push(`${summary.duplicate_rows} duplicate row(s) skipped`);
  }
  if (summary.skipped_jobs) {
    messageParts.push(`${summary.skipped_jobs} job(s) skipped`);
  }
  if (summary.skipped_rows) {
    messageParts.push(`${summary.skipped_rows} row(s) skipped`);
  }

  return `${messageParts.join(", ")}.`;
}

function escapeHistoryHtml(value) {
  const div = document.createElement("div");
  div.textContent = value == null ? "" : String(value);
  return div.innerHTML;
}

function buildHistoryDropdownButtonMarkup(spoolMeta, arrow = "▼") {
  const swatchColor = spoolMeta.colorHex ? `#${spoolMeta.colorHex}` : "#ccc";
  const detailMarkup = spoolMeta.detail
    ? `<span class="history-dropdown-detail" title="${escapeHistoryHtml(spoolMeta.detail)}">${escapeHistoryHtml(spoolMeta.detail)}</span>`
    : "";

  return `
        <div class="history-dropdown-value">
            <div class="color-swatch" style="background-color: ${swatchColor};"></div>
            <div class="history-dropdown-copy">
                <span class="history-dropdown-label" title="${escapeHistoryHtml(spoolMeta.label)}">${escapeHistoryHtml(spoolMeta.label)}</span>
                ${detailMarkup}
            </div>
        </div>
        <span class="dropdown-arrow">${escapeHistoryHtml(arrow)}</span>
    `;
}

function closeHistoryDropdown(dropdown) {
  if (!dropdown) {
    return;
  }

  const content = dropdown.querySelector(".dropdown-content");
  const button = dropdown.querySelector(".dropdown-button");
  const arrow = dropdown.querySelector(".dropdown-arrow");
  const searchInput = dropdown.querySelector(".dropdown-search");
  const noResults = dropdown.querySelector(".dropdown-no-results");

  if (content) {
    content.classList.remove("show");
  }
  if (button) {
    button.classList.remove("open");
  }
  if (arrow) {
    arrow.classList.remove("open");
  }
  if (searchInput) {
    searchInput.value = "";
  }
  if (noResults) {
    noResults.style.display = "none";
  }

  dropdown.querySelectorAll(".dropdown-option").forEach((option) => {
    option.style.display = "flex";
  });
}

function closeAllHistoryDropdowns(exceptDropdown) {
  document.querySelectorAll(".history-dropdown").forEach((dropdown) => {
    if (dropdown !== exceptDropdown) {
      closeHistoryDropdown(dropdown);
    }
  });
}

function updateHistoryDropdownSelection(dropdown, spoolId, spoolMeta) {
  const hiddenInput = dropdown.querySelector('input[type="hidden"]');
  const button = dropdown.querySelector(".dropdown-button");

  if (hiddenInput) {
    hiddenInput.value = spoolId == null ? "" : String(spoolId);
  }

  if (button) {
    button.innerHTML = buildHistoryDropdownButtonMarkup(spoolMeta);
  }

  dropdown.querySelectorAll(".dropdown-option").forEach((option) => {
    option.classList.toggle(
      "selected",
      (option.dataset.value || "") === (hiddenInput ? hiddenInput.value : ""),
    );
  });
}

function createHistoryDropdownOption(spoolId, spoolMeta) {
  const option = document.createElement("div");
  option.className = "dropdown-option";
  option.dataset.value = spoolId == null ? "" : String(spoolId);
  option.dataset.color = spoolMeta.colorHex || "";
  option.dataset.label = spoolMeta.label;
  option.dataset.detail = spoolMeta.detail;

  const swatch = document.createElement("div");
  swatch.className = "color-swatch";
  swatch.style.backgroundColor = spoolMeta.colorHex
    ? `#${spoolMeta.colorHex}`
    : "#ccc";

  const optionText = document.createElement("div");
  optionText.className = "option-text";
  optionText.textContent = `${spoolMeta.label}${spoolMeta.detail ? ` (${spoolMeta.detail})` : ""}`;

  option.appendChild(swatch);
  option.appendChild(optionText);

  return option;
}

function createHistorySpoolDropdown(entry, spoolsById, row) {
  const dropdown = document.createElement("div");
  dropdown.className = "history-dropdown";

  const hiddenInput = document.createElement("input");
  hiddenInput.type = "hidden";
  hiddenInput.value = entry.spool_id == null ? "" : String(entry.spool_id);

  const selectedSpoolMeta = getHistorySpoolMeta(entry.spool_id, spoolsById);
  const button = document.createElement("div");
  button.className = "dropdown-button";
  button.innerHTML = buildHistoryDropdownButtonMarkup(selectedSpoolMeta);

  const content = document.createElement("div");
  content.className = "dropdown-content";

  const searchContainer = document.createElement("div");
  searchContainer.className = "dropdown-search-container";

  const searchInput = document.createElement("input");
  searchInput.type = "text";
  searchInput.className = "dropdown-search";
  searchInput.placeholder = "Search spools...";
  searchInput.autocomplete = "off";
  searchContainer.appendChild(searchInput);

  const optionsContainer = document.createElement("div");
  optionsContainer.className = "dropdown-options-container";

  const unknownMeta = getHistorySpoolMeta(null, spoolsById);
  optionsContainer.appendChild(createHistoryDropdownOption(null, unknownMeta));

  if (entry.spool_id != null && !spoolsById.has(entry.spool_id)) {
    const missingMeta = getHistorySpoolMeta(entry.spool_id, spoolsById);
    optionsContainer.appendChild(
      createHistoryDropdownOption(entry.spool_id, missingMeta),
    );
  }

  Array.from(spoolsById.values())
    .sort((left, right) => left.id - right.id)
    .forEach((spool) => {
      optionsContainer.appendChild(
        createHistoryDropdownOption(
          spool.id,
          getHistorySpoolMeta(spool.id, spoolsById),
        ),
      );
    });

  const noResults = document.createElement("div");
  noResults.className = "dropdown-no-results";
  noResults.textContent = "No spools found";
  optionsContainer.appendChild(noResults);

  content.appendChild(searchContainer);
  content.appendChild(optionsContainer);
  dropdown.appendChild(button);
  dropdown.appendChild(content);
  dropdown.appendChild(hiddenInput);

  updateHistoryDropdownSelection(dropdown, entry.spool_id, selectedSpoolMeta);

  button.addEventListener("click", function (event) {
    event.stopPropagation();

    const isOpening = !content.classList.contains("show");
    closeAllHistoryDropdowns(dropdown);
    content.classList.toggle("show", isOpening);
    button.classList.toggle("open", isOpening);

    const arrow = button.querySelector(".dropdown-arrow");
    if (arrow) {
      arrow.classList.toggle("open", isOpening);
    }

    if (isOpening) {
      setTimeout(function () {
        searchInput.focus();
      }, 10);
    }
  });

  searchInput.addEventListener("click", function (event) {
    event.stopPropagation();
  });

  searchInput.addEventListener("input", function () {
    const searchTerm = searchInput.value.toLowerCase().trim();
    let visibleCount = 0;

    optionsContainer.querySelectorAll(".dropdown-option").forEach((option) => {
      const optionText =
        option.querySelector(".option-text")?.textContent.toLowerCase() || "";
      let isMatch = searchTerm === "";

      if (searchTerm !== "") {
        if (/^\d+$/.test(searchTerm)) {
          const idMatch = optionText.match(/^spool #(\d+)|^\[(\d+)\]/);
          isMatch = Boolean(
            idMatch && (idMatch[1] === searchTerm || idMatch[2] === searchTerm),
          );
        } else {
          const escapedSearch = searchTerm.replace(
            /[.*+?^${}()|[\]\\]/g,
            "\\$&",
          );
          const searchRegex = new RegExp(`\\b${escapedSearch}`, "i");
          isMatch = searchRegex.test(optionText);
        }
      }

      option.style.display = isMatch ? "flex" : "none";
      if (isMatch) {
        visibleCount++;
      }
    });

    noResults.style.display =
      visibleCount === 0 && searchTerm !== "" ? "block" : "none";
  });

  optionsContainer.querySelectorAll(".dropdown-option").forEach((option) => {
    option.addEventListener("click", function (event) {
      event.stopPropagation();

      const optionValue = option.dataset.value || "";
      const optionMeta = {
        label: option.dataset.label || "Unknown spool",
        detail: option.dataset.detail || "",
        colorHex: option.dataset.color || "",
      };

      updateHistoryDropdownSelection(
        dropdown,
        optionValue === "" ? null : optionValue,
        optionMeta,
      );
      closeHistoryDropdown(dropdown);
      updateHistoryRowState(row);
    });
  });

  return dropdown;
}

function updateHistoryRowState(row) {
  const select = row.querySelector('.history-action input[type="hidden"]');
  const weightInput = row.querySelector(".history-weight-input");
  const button = row.querySelector(".history-save-btn");
  const action = row.querySelector(".history-action");
  if (!select || !weightInput || !button || !action) {
    return;
  }

  const parsedWeight = parseHistoryWeightInput(weightInput);
  const hasSpoolChanged = select.value !== row.dataset.currentSpoolId;
  const hasWeightChanged =
    parsedWeight.normalized !== (row.dataset.currentFilamentUsed || "");
  const hasChanged = hasSpoolChanged || hasWeightChanged;

  weightInput.classList.toggle("invalid", !parsedWeight.valid);
  button.disabled = !hasChanged || !parsedWeight.valid;
  button.classList.toggle("hidden", !hasChanged);
  action.classList.toggle("has-pending-change", hasChanged);
}

function setHistoryActionBusy(row, busy) {
  if (!row) {
    return;
  }

  const controls = row.querySelectorAll(
    ".history-save-btn, .history-pull-btn, .history-weight-input",
  );
  controls.forEach((control) => {
    control.disabled = busy;
  });
}

function renderPrintHistory(history, spoolsById) {
  const tbody = document.getElementById("print-history-body");
  if (!tbody) {
    return;
  }

  tbody.innerHTML = "";

  if (!history.length) {
    const emptyRow = document.createElement("tr");
    const emptyCell = document.createElement("td");
    emptyCell.colSpan = 6;
    emptyCell.className = "history-empty";
    emptyCell.textContent = "No completed prints recorded yet.";
    emptyRow.appendChild(emptyCell);
    tbody.appendChild(emptyRow);
    return;
  }

  history.forEach((entry) => {
    const row = document.createElement("tr");
    row.dataset.historyId = entry.id;
    row.dataset.currentSpoolId =
      entry.spool_id == null ? "" : String(entry.spool_id);
    row.dataset.currentFilamentUsed = normalizeHistoryWeightValue(
      entry.filament_used,
    );

    const finishedCell = document.createElement("td");
    finishedCell.className = "history-col-finished";
    finishedCell.textContent = formatHistoryTimestamp(entry.print_finished);

    const usedCell = document.createElement("td");
    usedCell.className = "history-col-used";
    const usedEditor = document.createElement("div");
    usedEditor.className = "history-used-editor";

    const weightInput = document.createElement("input");
    weightInput.type = "number";
    weightInput.min = "0";
    weightInput.step = "1";
    weightInput.className = "history-weight-input";
    weightInput.placeholder = "0";
    if (entry.filament_used > 0) {
      weightInput.value = String(Math.round(entry.filament_used));
    }
    weightInput.addEventListener("input", function () {
      updateHistoryRowState(row);
    });

    usedEditor.appendChild(weightInput);
    usedCell.appendChild(usedEditor);

    const spoolCell = document.createElement("td");
    spoolCell.className = "history-col-spool history-spool-cell";

    const action = document.createElement("div");
    action.className = "history-action";
    const dropdown = createHistorySpoolDropdown(entry, spoolsById, row);

    const saveButton = document.createElement("button");
    saveButton.className = "btn btn-small history-save-btn hidden";
    saveButton.textContent = "Save";
    saveButton.disabled = true;
    saveButton.addEventListener("click", function () {
      const selectedValue =
        dropdown.querySelector('input[type="hidden"]')?.value || "";
      savePrintHistory(entry.id, selectedValue, weightInput, saveButton);
    });

    const pullButton = document.createElement("button");
    pullButton.className = "btn btn-small btn-secondary history-pull-btn";
    pullButton.textContent = "Pull";
    pullButton.addEventListener("click", function () {
      const selectedValue =
        dropdown.querySelector('input[type="hidden"]')?.value || "";
      pullPrintHistory(entry.id, selectedValue, row, pullButton);
    });

    action.appendChild(dropdown);
    action.appendChild(pullButton);
    action.appendChild(saveButton);
    spoolCell.appendChild(action);

    const jobCell = document.createElement("td");
    jobCell.className = "history-col-print history-job-cell";
    const jobName = document.createElement("div");
    jobName.className = "history-job";
    jobName.textContent = entry.job_name || "Unnamed Print";
    jobName.title = entry.job_name || "Unnamed Print";
    const jobMeta = document.createElement("span");
    jobMeta.className = "history-meta";
    jobMeta.textContent = `History #${entry.id}`;
    jobCell.appendChild(jobName);
    jobCell.appendChild(jobMeta);

    const printerCell = document.createElement("td");
    printerCell.className = "history-col-printer";
    printerCell.textContent = entry.printer_name;

    const toolheadCell = document.createElement("td");
    toolheadCell.className = "history-col-toolhead";
    toolheadCell.textContent =
      entry.toolhead_name || `Toolhead ${entry.toolhead_id}`;

    row.appendChild(finishedCell);
    row.appendChild(usedCell);
    row.appendChild(spoolCell);
    row.appendChild(jobCell);
    row.appendChild(printerCell);
    row.appendChild(toolheadCell);

    tbody.appendChild(row);
    updateHistoryRowState(row);
  });
}

async function importPrintHistory() {
  const { printerSelect, toolheadSelect, fileInput, importButton } =
    getHistoryImportElements();

  if (!printerSelect || !toolheadSelect || !importButton) {
    return;
  }

  const printerId = printerSelect.value;
  if (!printerId) {
    setHistoryFeedback("Select printer first.", "error");
    return;
  }

  let payload = "";
  try {
    payload = await readHistoryImportPayload();
  } catch (error) {
    setHistoryFeedback(
      `Failed to read import payload: ${error.message}`,
      "error",
    );
    return;
  }
  if (!payload) {
    setHistoryFeedback("Choose a Prusa Connect JSON file first.", "error");
    return;
  }

  importButton.disabled = true;
  const originalLabel = importButton.textContent;
  importButton.textContent = "Importing...";
  setHistoryFeedback("", "");

  return historyFetchJson("/api/print-history/import", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({
      printer_id: printerId,
      default_toolhead_id: parseInt(toolheadSelect.value || "0", 10),
      payload,
    }),
  })
    .then((response) => {
      setHistoryFeedback(
        formatHistoryImportSummary(response.summary),
        "success",
      );
      if (fileInput) {
        fileInput.value = "";
      }
      return loadPrintHistory();
    })
    .catch((error) => {
      setHistoryFeedback(
        `Failed to import print history: ${error.message}`,
        "error",
      );
    })
    .finally(() => {
      importButton.disabled = false;
      importButton.textContent = originalLabel;
    });
}

function loadPrintHistory() {
  const tbody = document.getElementById("print-history-body");
  const requestId = ++historyLoadCounter;

  if (tbody) {
    tbody.innerHTML =
      '<tr><td colspan="6" class="history-empty">Loading print history...</td></tr>';
  }

  return Promise.all([
    historyFetchJson("/api/print-history?limit=200"),
    historyFetchJson("/api/spools?include_empty=true"),
    loadHistoryImportOptions(),
  ])
    .then(([historyData, spools]) => {
      if (requestId !== historyLoadCounter) {
        return;
      }

      const spoolsById = new Map(spools.map((spool) => [spool.id, spool]));
      renderPrintHistory(historyData.history || [], spoolsById);
    })
    .catch((error) => {
      if (requestId !== historyLoadCounter) {
        return;
      }

      if (tbody) {
        tbody.innerHTML = "";
        const row = document.createElement("tr");
        const cell = document.createElement("td");
        cell.colSpan = 6;
        cell.className = "history-empty";
        cell.textContent = `Failed to load print history: ${error.message}`;
        row.appendChild(cell);
        tbody.appendChild(row);
      }
    });
}

document.addEventListener("DOMContentLoaded", function () {
  const { printerSelect } = getHistoryImportElements();
  if (printerSelect) {
    printerSelect.addEventListener("change", function () {
      renderHistoryToolheadOptions(printerSelect.value);
    });
  }

  document.addEventListener("click", function () {
    closeAllHistoryDropdowns();
  });
});

function savePrintHistory(historyId, spoolId, weightInput, button) {
  const parsedWeight = parseHistoryWeightInput(weightInput);
  if (!parsedWeight.valid) {
    setHistoryFeedback(
      "Filament used must be a number greater than or equal to 0.",
      "error",
    );
    updateHistoryRowState(button.closest("tr"));
    return Promise.resolve();
  }

  button.disabled = true;
  const originalLabel = button.textContent;
  button.textContent = "Saving...";
  setHistoryFeedback("", "");

  return historyFetchJson(`/api/print-history/${historyId}`, {
    method: "PUT",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({
      spool_id: spoolId === "" ? null : parseInt(spoolId, 10),
      filament_used: parsedWeight.value,
    }),
  })
    .then(() => {
      setHistoryFeedback("Print history updated.", "success");
      return loadPrintHistory();
    })
    .catch((error) => {
      setHistoryFeedback(
        `Failed to update print history: ${error.message}`,
        "error",
      );
    })
    .finally(() => {
      button.textContent = originalLabel;
    });
}

function pullPrintHistory(historyId, spoolId, row, button) {
  setHistoryActionBusy(row, true);
  const originalLabel = button.textContent;
  button.textContent = "Pulling...";
  setHistoryFeedback("", "");

  return historyFetchJson(`/api/print-history/${historyId}/pull`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({
      spool_id: spoolId === "" ? null : parseInt(spoolId, 10),
    }),
  })
    .then((response) => {
      const pulledValue = response?.entry?.filament_used;
      if (Number.isFinite(pulledValue) && pulledValue > 0) {
        setHistoryFeedback(
          `Pulled ${normalizeHistoryWeightValue(pulledValue)}g from printer history.`,
          "success",
        );
      } else {
        setHistoryFeedback("Pulled print history from printer.", "success");
      }
      return loadPrintHistory();
    })
    .catch((error) => {
      setHistoryFeedback(
        `Failed to pull print history from printer: ${error.message}`,
        "error",
      );
    })
    .finally(() => {
      button.textContent = originalLabel;
      setHistoryActionBusy(row, false);
      updateHistoryRowState(row);
    });
}
