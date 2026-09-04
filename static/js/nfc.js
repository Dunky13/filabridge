// FilaBridge Dashboard - NFC Management Functions

// NFC Management Functions
function switchNfcTab(tabName, clickedElement) {
    console.log('Switching to NFC tab:', tabName);
    // Hide all NFC tab contents
    document.querySelectorAll('.nfc-tab-content').forEach(tab => {
        tab.classList.remove('active');
    });
    
    // Remove active class from all NFC tabs
    document.querySelectorAll('.nfc-tab').forEach(tab => {
        tab.classList.remove('active');
    });
    
    // Show selected tab content
    document.getElementById(tabName + '-tab').classList.add('active');
    
    // Add active class to clicked tab
    if (clickedElement) {
        clickedElement.classList.add('active');
    } else {
        // Fallback: find the tab button by onclick attribute
        const tabButtons = document.querySelectorAll('.nfc-tab');
        tabButtons.forEach(btn => {
            if (btn.getAttribute('onclick').includes(tabName)) {
                btn.classList.add('active');
            }
        });
    }
    
    // Load data for specific tabs
    if (tabName === 'spool-tags') {
        console.log('Loading spool tags...');
        loadSpoolTags();
    } else if (tabName === 'filament-tags') {
        console.log('Loading filament tags...');
        loadFilamentTags();
    } else if (tabName === 'location-tags') {
        console.log('Loading location tags...');
        loadLocationTags();
    }
}

async function loadNfcData() {
    await loadSpoolTags();
    await loadFilamentTags();
    await loadLocationTags();
}

function normalizedNfcColor(value) {
    const color = String(value || '').replace(/^#/, '');
    return /^(?:[0-9a-fA-F]{3}|[0-9a-fA-F]{6}|[0-9a-fA-F]{8})$/.test(color)
        ? `#${color}`
        : '#ccc';
}

function appendNfcListCopy(item, name, details) {
    const info = document.createElement('div');
    info.className = 'item-info';
    const nameElement = document.createElement('div');
    nameElement.className = 'item-name';
    nameElement.textContent = name;
    info.appendChild(nameElement);
    if (details != null) {
        const detailsElement = document.createElement('div');
        detailsElement.className = 'item-details';
        detailsElement.textContent = details;
        info.appendChild(detailsElement);
    }
    item.appendChild(info);
}

async function loadSpoolTags() {
    try {
        console.log('Loading spool tags...');
        const response = await fetch('/api/nfc/urls');
        const data = await response.json();
        console.log('NFC URLs data:', data);
        
        const container = document.getElementById('spool-list-container');
        const spoolUrls = data.urls.filter(url => url.type === 'spool');
        console.log('Spool URLs:', spoolUrls);
        
        if (spoolUrls.length === 0) {
            const message = document.createElement('p');
            message.textContent = 'No spools available';
            container.replaceChildren(message);
            return;
        }
        
        container.replaceChildren();
        
        spoolUrls.forEach(url => {
            const item = document.createElement('div');
            item.className = 'nfc-list-item';
            item.dataset.value = url.spool_id;
            item.dataset.color = url.color_hex;
            item.dataset.url = url.url;
            item.dataset.qr = url.qr_code_base64;
            
            const colorSwatch = document.createElement('div');
            colorSwatch.className = 'color-swatch';
            colorSwatch.style.backgroundColor = normalizedNfcColor(url.color_hex);
            item.appendChild(colorSwatch);
            const remaining = url.remaining_weight != null && Number.isFinite(Number(url.remaining_weight))
                ? ` - ${Math.round(Number(url.remaining_weight))}g remaining`
                : '';
            appendNfcListCopy(
                item,
                `[${String(url.spool_id ?? '')}] ${String(url.spool_name || '')}`,
                `${String(url.material || '')} - ${String(url.brand || '')}${remaining}`,
            );
            
            // Add click handler
            item.addEventListener('click', () => {
                // Remove selected class from all items
                container.querySelectorAll('.nfc-list-item').forEach(i => i.classList.remove('selected'));
                // Add selected class to clicked item
                item.classList.add('selected');
                // Show QR code
                displaySpoolQR(url);
            });
            
            container.appendChild(item);
        });
        
        // Initialize search functionality
        initializeSpoolSearch(spoolUrls);
        
    } catch (error) {
        console.error('Error loading spool tags:', error);
        const message = document.createElement('p');
        message.textContent = 'Error loading spools';
        document.getElementById('spool-list-container').replaceChildren(message);
    }
}

async function loadFilamentTags() {
    try {
        console.log('Loading filament tags...');
        const response = await fetch('/api/nfc/urls');
        const data = await response.json();
        console.log('NFC URLs data:', data);
        
        const container = document.getElementById('filament-list-container');
        const filamentUrls = data.urls.filter(url => url.type === 'filament');
        console.log('Filament URLs:', filamentUrls);
        
        if (filamentUrls.length === 0) {
            const message = document.createElement('p');
            message.textContent = 'No filaments available';
            container.replaceChildren(message);
            return;
        }
        
        container.replaceChildren();
        
        filamentUrls.forEach(url => {
            const item = document.createElement('div');
            item.className = 'nfc-list-item';
            item.dataset.value = url.filament_id;
            item.dataset.color = url.color_hex;
            item.dataset.url = url.url;
            item.dataset.qr = url.qr_code_base64;
            
            const colorSwatch = document.createElement('div');
            colorSwatch.className = 'color-swatch';
            colorSwatch.style.backgroundColor = normalizedNfcColor(url.color_hex);
            item.appendChild(colorSwatch);
            appendNfcListCopy(
                item,
                String(url.filament_name || ''),
                `${String(url.material || '')} - ${String(url.brand || '')}`,
            );
            
            // Add click handler
            item.addEventListener('click', () => {
                // Remove selected class from all items
                container.querySelectorAll('.nfc-list-item').forEach(i => i.classList.remove('selected'));
                // Add selected class to clicked item
                item.classList.add('selected');
                // Show QR code
                displayFilamentQR(url);
            });
            
            container.appendChild(item);
        });
        
        // Initialize search functionality
        initializeFilamentSearch(filamentUrls);
        
    } catch (error) {
        console.error('Error loading filament tags:', error);
        const message = document.createElement('p');
        message.textContent = 'Error loading filaments';
        document.getElementById('filament-list-container').replaceChildren(message);
    }
}

async function loadLocationTags() {
    try {
        console.log('Loading location tags...');
        const response = await fetch('/api/nfc/urls');
        const data = await response.json();
        console.log('NFC URLs data:', data);
        
        const container = document.getElementById('location-list-container');
        const locationUrls = data.urls.filter(url => url.type === 'location');
        console.log('Location URLs:', locationUrls);
        
        // Clear container and add informational message
        container.replaceChildren();
        
        // Add informational banner about Spoolman locations
        const spoolmanURL = data.spoolman_url || '';
        const messageBanner = document.createElement('div');
        messageBanner.className = 'nfc-info-banner';
        messageBanner.style.cssText = 'background: #fff3cd; border: 1px solid #ffeaa7; color: #856404; padding: 15px; margin-bottom: 15px; border-radius: 8px;';
        
        const bannerHeading = document.createElement('strong');
        bannerHeading.textContent = 'ℹ️ Location Management:';
        messageBanner.append(bannerHeading, document.createElement('br'));
        messageBanner.appendChild(document.createTextNode(
            'It is not possible via the Spoolman API to add locations automatically. ' +
            'To create locations, please do so via Spoolman. Then they will show up here.',
        ));
        
        if (spoolmanURL) {
            try {
                const parsedURL = new URL(spoolmanURL);
                if (parsedURL.protocol === 'http:' || parsedURL.protocol === 'https:') {
                    const link = document.createElement('a');
                    link.href = new URL('/locations', parsedURL).href;
                    link.target = '_blank';
                    link.rel = 'noopener noreferrer';
                    link.style.cssText = 'color: #856404; text-decoration: underline; font-weight: bold;';
                    link.textContent = 'Open Spoolman →';
                    messageBanner.append(document.createElement('br'), document.createElement('br'), link);
                }
            } catch (error) {
                console.warn('Ignoring invalid Spoolman URL in NFC response', error);
            }
        }
        
        container.appendChild(messageBanner);
        
        if (locationUrls.length === 0) {
            const noLocationsMsg = document.createElement('p');
            noLocationsMsg.textContent = 'No locations available. Create locations in Spoolman to see them here.';
            noLocationsMsg.style.cssText = 'padding: 20px; text-align: center; color: #666;';
            container.appendChild(noLocationsMsg);
            return;
        }
        
        locationUrls.forEach(url => {
            const item = document.createElement('div');
            item.className = 'nfc-list-item';
            item.dataset.value = url.display_name;
            item.dataset.url = url.url;
            item.dataset.qr = url.qr_code_base64;
            
            const locationIcon = document.createElement('div');
            locationIcon.className = 'location-icon';
            if (url.location_type === 'printer') {
                const image = document.createElement('img');
                image.src = '/static/images/3d-printer-icon.png';
                image.alt = '3D Printer';
                image.style.cssText = 'width: 20px; height: 20px;';
                locationIcon.appendChild(image);
            } else {
                locationIcon.textContent = '📦';
            }
            item.appendChild(locationIcon);
            appendNfcListCopy(item, String(url.display_name || ''), null);
            const actions = createLocationActions(url);
            if (actions) item.appendChild(actions);
            
            // Add click handler
            item.addEventListener('click', (e) => {
                // Don't trigger if clicking on action buttons
                if (e.target.closest('.location-actions')) {
                    return;
                }
                
                // Remove selected class from all items
                container.querySelectorAll('.nfc-list-item').forEach(i => i.classList.remove('selected'));
                // Add selected class to clicked item
                item.classList.add('selected');
                // Show QR code
                displayLocationQR({
                    name: url.display_name,
                    is_printer_location: url.location_type === 'printer',
                    url: url.url,
                    qr_code_base64: url.qr_code_base64,
                    description: url.description || ''
                });
            });
            
            container.appendChild(item);
        });
        
        // Initialize search functionality
        initializeLocationSearch(locationUrls);
        
    } catch (error) {
        console.error('Error loading location tags:', error);
        const message = document.createElement('p');
        message.textContent = 'Error loading locations';
        document.getElementById('location-list-container').replaceChildren(message);
    }
}

// Create actions without putting location names into HTML or handler strings.
function createLocationActions(url) {
    if (url.location_type === 'printer') return null;
    const name = String(url.display_name || '');
    const actions = document.createElement('div');
    actions.className = 'location-actions';
    actions.style.cssText = 'margin-left: 8px; font-weight: normal;';

    const rename = document.createElement('button');
    rename.type = 'button';
    rename.className = 'nfc-link-button';
    rename.style.cssText = 'background: none; border: 0; padding: 0; color: inherit; text-decoration: underline; cursor: pointer; font: inherit;';
    rename.textContent = 'Rename';
    rename.addEventListener('click', event => {
        event.preventDefault();
        event.stopPropagation();
        renameLocation(name);
    });
    actions.appendChild(rename);

    if (url.is_local_only) {
        actions.appendChild(document.createTextNode(' • '));
        const remove = document.createElement('button');
        remove.type = 'button';
        remove.className = 'nfc-link-button';
        remove.style.cssText = 'background: none; border: 0; padding: 0; color: #ff6b6b; text-decoration: underline; cursor: pointer; font: inherit;';
        remove.textContent = 'Delete';
        remove.addEventListener('click', event => {
            event.preventDefault();
            event.stopPropagation();
            deleteLocation(name);
        });
        actions.appendChild(remove);
    } else {
        const synced = document.createElement('span');
        synced.style.cssText = 'color: #666; font-size: 0.9em;';
        synced.textContent = ' (Synced to Spoolman)';
        actions.appendChild(synced);
    }
    return actions;
}

// Copy URL to clipboard
async function copyUrlToClipboard(urlElementId, buttonElement) {
    try {
        const urlElement = document.getElementById(urlElementId);
        const url = urlElement.textContent;
        
        if (!url) {
            console.warn('No URL to copy');
            return;
        }
        
        // Use the Clipboard API
        await navigator.clipboard.writeText(url);
        
        // Visual feedback - change icon temporarily
        const icon = buttonElement.querySelector('.nfc-copy-icon');
        const originalIcon = icon.textContent;
        icon.textContent = '✓';
        buttonElement.style.background = 'rgba(76, 175, 80, 0.3)';
        
        // Reset after 2 seconds
        setTimeout(() => {
            icon.textContent = originalIcon;
            buttonElement.style.background = '';
        }, 2000);
        
    } catch (err) {
        console.error('Failed to copy URL:', err);
        // Fallback for older browsers
        const urlElement = document.getElementById(urlElementId);
        const url = urlElement.textContent;
        const textArea = document.createElement('textarea');
        textArea.value = url;
        textArea.style.position = 'fixed';
        textArea.style.opacity = '0';
        document.body.appendChild(textArea);
        textArea.select();
        try {
            document.execCommand('copy');
            const icon = buttonElement.querySelector('.nfc-copy-icon');
            const originalIcon = icon.textContent;
            icon.textContent = '✓';
            buttonElement.style.background = 'rgba(76, 175, 80, 0.3)';
            setTimeout(() => {
                icon.textContent = originalIcon;
                buttonElement.style.background = '';
            }, 2000);
        } catch (fallbackErr) {
            console.error('Fallback copy failed:', fallbackErr);
            alert('Failed to copy URL. Please copy manually.');
        }
        document.body.removeChild(textArea);
    }
}

// Display QR code for selected spool
function displaySpoolQR(spoolData) {
    console.log('Displaying spool QR:', spoolData);
    
    // Hide no-selection message
    document.getElementById('spool-no-selection').style.display = 'none';
    
    // Show QR display
    const display = document.getElementById('spool-qr-display');
    display.style.display = 'block';
    
    // Update content
    document.getElementById('spool-selected-name').textContent = `[${spoolData.spool_id}] ${spoolData.spool_name}`;
    document.getElementById('spool-selected-details').replaceChildren();
    document.getElementById('spool-qr-large').src = `data:image/png;base64,${spoolData.qr_code_base64}`;
    document.getElementById('spool-url-text').textContent = spoolData.url;
}

// Display QR code for selected filament
function displayFilamentQR(filamentData) {
    console.log('Displaying filament QR:', filamentData);
    
    // Hide no-selection message
    document.getElementById('filament-no-selection').style.display = 'none';
    
    // Show QR display
    const display = document.getElementById('filament-qr-display');
    display.style.display = 'block';
    
    // Update content
    document.getElementById('filament-selected-name').textContent = filamentData.filament_name;
    document.getElementById('filament-selected-details').replaceChildren();
    document.getElementById('filament-qr-large').src = `data:image/png;base64,${filamentData.qr_code_base64}`;
    document.getElementById('filament-url-text').textContent = filamentData.url;
}

// Display QR code for selected location
function displayLocationQR(locationData) {
    console.log('Displaying location QR:', locationData);
    
    // Hide no-selection message
    document.getElementById('location-no-selection').style.display = 'none';
    
    // Show QR display
    const display = document.getElementById('location-qr-display');
    display.style.display = 'block';
    
    // Update content
    document.getElementById('location-selected-name').textContent = locationData.name;
    const details = document.getElementById('location-selected-details');
    details.replaceChildren();
    const typeLabel = document.createElement('strong');
    typeLabel.textContent = 'Type:';
    details.append(typeLabel, document.createTextNode(
        ` ${locationData.is_printer_location ? 'Printer Location' : 'Custom Location'}`,
    ), document.createElement('br'));
    if (locationData.description) {
        const descriptionLabel = document.createElement('strong');
        descriptionLabel.textContent = 'Description:';
        details.append(descriptionLabel, document.createTextNode(` ${String(locationData.description)}`), document.createElement('br'));
    }
    document.getElementById('location-qr-large').src = `data:image/png;base64,${locationData.qr_code_base64}`;
    document.getElementById('location-url-text').textContent = locationData.url;
}

// Initialize search functionality for spools
function initializeSpoolSearch(spoolUrls) {
    const searchInput = document.getElementById('spool-search');
    const container = document.getElementById('spool-list-container');
    
    searchInput.addEventListener('input', (e) => {
        const searchTerm = e.target.value.toLowerCase();
        const items = container.querySelectorAll('.nfc-list-item');
        
        items.forEach(item => {
            const name = item.querySelector('.item-name').textContent.toLowerCase();
            const details = item.querySelector('.item-details').textContent.toLowerCase();
            
            if (name.includes(searchTerm) || details.includes(searchTerm)) {
                item.style.display = 'flex';
            } else {
                item.style.display = 'none';
            }
        });
    });
}

// Initialize search functionality for filaments
function initializeFilamentSearch(filamentUrls) {
    const searchInput = document.getElementById('filament-search');
    const container = document.getElementById('filament-list-container');
    
    searchInput.addEventListener('input', (e) => {
        const searchTerm = e.target.value.toLowerCase();
        const items = container.querySelectorAll('.nfc-list-item');
        
        items.forEach(item => {
            const name = item.querySelector('.item-name').textContent.toLowerCase();
            const details = item.querySelector('.item-details').textContent.toLowerCase();
            
            if (name.includes(searchTerm) || details.includes(searchTerm)) {
                item.style.display = 'flex';
            } else {
                item.style.display = 'none';
            }
        });
    });
}

// Initialize search functionality for locations
function initializeLocationSearch(locationUrls) {
    const searchInput = document.getElementById('location-search');
    const container = document.getElementById('location-list-container');
    
    searchInput.addEventListener('input', (e) => {
        const searchTerm = e.target.value.toLowerCase();
        const items = container.querySelectorAll('.nfc-list-item');
        
        items.forEach(item => {
            const name = item.querySelector('.item-name').textContent.toLowerCase();
            const details = item.querySelector('.item-details').textContent.toLowerCase();
            
            if (name.includes(searchTerm) || details.includes(searchTerm)) {
                item.style.display = 'flex';
            } else {
                item.style.display = 'none';
            }
        });
    });
}

// Location Management Functions
async function addLocation() {
    const nameEl = document.getElementById('newLocationName');
    const name = (nameEl.value || '').trim();
    if (!name) { alert('Please enter a location name'); return; }
    try {
        const url = apiUrl('/api/locations');
        console.log('POST', url, { name });
        const res = await fetch(url, {
            method: 'POST',
            headers: { 'Content-Type': 'application/json', 'Accept': 'application/json' },
            mode: 'same-origin', credentials: 'same-origin',
            body: JSON.stringify({ name })
        });
        if (!res.ok) throw new Error(await res.text());
        nameEl.value = '';
        await loadLocationTags();
    } catch (e) { console.error(e); alert(e.message || 'Network error'); }
}

async function renameLocation(currentName) {
    const newName = prompt('Rename location', currentName || '');
    if (!newName || newName.trim() === '' || newName === currentName) return;
    try {
        const url = apiUrl(`/api/locations/${encodeURIComponent(currentName)}`);
        console.log('PUT', url, { name: newName.trim() });
        const res = await fetch(url, {
            method: 'PUT',
            headers: { 'Content-Type': 'application/json', 'Accept': 'application/json' },
            mode: 'same-origin', credentials: 'same-origin',
            body: JSON.stringify({ name: newName.trim() })
        });
        if (!res.ok) {
            const errorText = await res.text();
            throw new Error(errorText);
        }
        const result = await res.json();
        console.log('Rename result:', result);
        await loadLocationTags();
        if (result.message) {
            alert(result.message);
        }
    } catch (e) { 
        console.error('Rename error:', e); 
        alert(e.message || 'Network error'); 
    }
}

async function deleteLocation(name) {
    try {
        console.log('deleteLocation called with name:', name);
        const url = apiUrl(`/api/locations/${encodeURIComponent(name)}`);
        console.log('DELETE', url);
        const res = await fetch(url, {
            method: 'DELETE',
            headers: { 'Accept': 'application/json' },
            mode: 'same-origin', credentials: 'same-origin'
        });
        if (!res.ok) {
            const errorText = await res.text();
            throw new Error(errorText);
        }
        const result = await res.json();
        console.log('Delete result:', result);
        await loadLocationTags();
    } catch (e) { 
        console.error('Delete error:', e); 
        alert(e.message || 'Network error'); 
    }
}


// QR Code Modal Functions
function showQrCode(url, title, qrCodeBase64) {
    // Create modal if it doesn't exist
    let modal = document.getElementById('nfc-qr-modal');
    if (!modal) {
        modal = document.createElement('div');
        modal.id = 'nfc-qr-modal';
        modal.className = 'nfc-qr-modal';
        modal.innerHTML = `
            <div class="nfc-qr-content">
                <h3 id="qr-title"></h3>
                <div class="nfc-qr-modal-code" id="qr-code"></div>
                <div class="nfc-url" id="qr-url"></div>
                <div class="nfc-instructions">
                    <h4>Instructions:</h4>
                    <ol>
                        <li>Open NFC Tools Pro on your phone</li>
                        <li>Scan this QR code to copy the URL</li>
                        <li>Write the URL to your NFC tag</li>
                    </ol>
                </div>
                <button class="btn" onclick="closeQrModal()">Close</button>
            </div>
        `;
        document.body.appendChild(modal);
    }
    
    // Update modal content
    document.getElementById('qr-title').textContent = title;
    document.getElementById('qr-url').textContent = url;
    
    // Display real QR code or placeholder
    const qrCodeDiv = document.getElementById('qr-code');
    qrCodeDiv.replaceChildren();
    if (typeof qrCodeBase64 === 'string' && /^[A-Za-z0-9+/]+={0,2}$/.test(qrCodeBase64)) {
        const image = document.createElement('img');
        image.src = `data:image/png;base64,${qrCodeBase64}`;
        image.alt = 'QR Code';
        image.style.cssText = 'width: 256px; height: 256px; border-radius: 8px; box-shadow: 0 4px 12px rgba(0,0,0,0.15);';
        qrCodeDiv.appendChild(image);
    } else {
        // Fallback placeholder if QR code generation failed
        const placeholder = document.createElement('div');
        placeholder.style.cssText = 'width: 256px; height: 256px; background: #f0f0f0; display: flex; align-items: center; justify-content: center; border: 2px dashed #ccc; border-radius: 8px; text-align: center;';
        const copy = document.createElement('div');
        for (const [text, style] of [
            ['⚠️', 'font-size: 48px; margin-bottom: 10px;'],
            ['QR Code Error', 'font-size: 12px; color: #666;'],
            ['Copy URL manually', 'font-size: 10px; color: #999;'],
        ]) {
            const line = document.createElement('div');
            line.style.cssText = style;
            line.textContent = text;
            copy.appendChild(line);
        }
        placeholder.appendChild(copy);
        qrCodeDiv.appendChild(placeholder);
    }
    
    // Show modal
    modal.style.display = 'block';
}

function closeQrModal() {
    const modal = document.getElementById('nfc-qr-modal');
    if (modal) {
        modal.style.display = 'none';
    }
}

// Close modal when clicking outside
window.onclick = function(event) {
    const modal = document.getElementById('nfc-qr-modal');
    if (event.target === modal) {
        closeQrModal();
    }
}
