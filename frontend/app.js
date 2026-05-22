(function(){
	const e = React.createElement;
	const API_BASE = window.PORTSCOPE_API_BASE || 'http://127.0.0.1:8080';

	function apiUrl(path) {
		return `${API_BASE}${path}`;
	}

function wsUrl(path) {
	const url = new URL(path, API_BASE);
	url.protocol = url.protocol === 'https:' ? 'wss:' : 'ws:';
	return url.toString();
}

function pointFromGeometry(geom) {
	if (!geom) return null;
	const parsed = typeof geom === 'string' ? JSON.parse(geom) : geom;
	if (parsed && parsed.type === 'Point' && Array.isArray(parsed.coordinates) && parsed.coordinates.length >= 2) {
		return parsed.coordinates;
	}
	return null;
}

function formatDate(value) {
	if (!value) return 'Unknown';
	return new Date(value).toLocaleDateString([], { month: 'short', day: 'numeric', year: 'numeric' });
}

function formatCount(value) {
	if (value === null || value === undefined || value === '') return 'n/a';
	const number = Number(value);
	return Number.isFinite(number) ? number.toLocaleString() : String(value);
}

function formatPercent(value) {
	if (value === null || value === undefined || value === '') return 'n/a';
	const number = Number(value);
	return Number.isFinite(number) ? `${number.toFixed(1)}%` : String(value);
}

function normalizeFeatureCollection(collection, kind) {
	if (!collection || !Array.isArray(collection.features)) return [];
	return collection.features.map((feature, index) => {
		const properties = feature.properties || {};
		return {
			id: properties.ObjectId || properties.portid || index + 1,
			pageid: properties.pageid || '',
			name: properties.fullname || properties.portname || properties.portid || `Unnamed ${kind}`,
			country: properties.country || '',
			iso3: properties.ISO3 || '',
			observed_on: properties.date || properties.observed_on || null,
			source_value: properties,
			geom: feature.geometry,
			kind,
		};
	});
}

function App() {
	const [ports, setPorts] = React.useState([]);
	const [chokepoints, setChokepoints] = React.useState([]);
	const [selectedItem, setSelectedItem] = React.useState(null);
	const [status, setStatus] = React.useState('Loading PortWatch datasets...');

	const mapRef = React.useRef(null);
	const portLayerRef = React.useRef(null);
	const chokepointLayerRef = React.useRef(null);

	function selectItem(type, data) {
		setSelectedItem({ type, data });
		setStatus(`${data.name} selected.`);
	}

	function renderLayers() {
		if (!portLayerRef.current || !chokepointLayerRef.current) return;
		portLayerRef.current.clearLayers();
		chokepointLayerRef.current.clearLayers();

		const hasSelection = Boolean(selectedItem);

		ports.forEach(item => {
			const coords = pointFromGeometry(item.geom);
			if (!coords) return;
			const [lon, lat] = coords;
			const active = hasSelection && selectedItem.type === 'port' && selectedItem.data.id === item.id;
			const marker = L.circleMarker([lat, lon], {
				radius: active ? 10 : 7,
				color: active ? '#63d6ff' : '#8a7dff',
				weight: 2,
				fillColor: active ? '#63d6ff' : '#c9c1ff',
				fillOpacity: 0.92,
			});
			marker.bindTooltip(`<b>${item.name}</b><br/>Port intelligence`, { direction: 'top', offset: [0, -8] });
			marker.on('click', () => selectItem('port', item));
			marker.addTo(portLayerRef.current);
		});

		chokepoints.forEach(item => {
			const coords = pointFromGeometry(item.geom);
			if (!coords) return;
			const [lon, lat] = coords;
			const active = hasSelection && selectedItem.type === 'chokepoint' && selectedItem.data.id === item.id;
			const marker = L.circleMarker([lat, lon], {
				radius: active ? 9 : 6,
				color: active ? '#ffcc66' : '#ff8a5b',
				weight: 2,
				fillColor: active ? '#ffcc66' : '#ffb38f',
				fillOpacity: 0.95,
			});
			marker.bindTooltip(`<b>${item.name}</b><br/>Chokepoint intensity`, { direction: 'top', offset: [0, -8] });
			marker.on('click', () => selectItem('chokepoint', item));
			marker.addTo(chokepointLayerRef.current);
		});
	}

	async function loadData() {
		setStatus('Loading local PortWatch datasets...');
		try {
			const [portsResponse, chokepointsResponse] = await Promise.all([
				fetch('/data/Ports.geojson'),
				fetch('/data/Chokepoints.geojson'),
			]);
			const [portsData, chokepointsData] = await Promise.all([
				portsResponse.json(),
				chokepointsResponse.json(),
			]);

			const normalizedPorts = normalizeFeatureCollection(portsData, 'port');
			const normalizedChokepoints = normalizeFeatureCollection(chokepointsData, 'chokepoint');

			setPorts(normalizedPorts);
			setChokepoints(normalizedChokepoints);

			if (normalizedPorts.length > 0 || normalizedChokepoints.length > 0) {
				setStatus(`Loaded ${normalizedPorts.length} ports and ${normalizedChokepoints.length} chokepoints.`);
			} else {
				setStatus('No local PortWatch records returned yet.');
			}
		} catch (err) {
			console.error(err);
			setStatus('Unable to load local PortWatch data.');
		}
	}

	React.useEffect(() => {
		if (typeof L === 'undefined') {
			setStatus('Leaflet library not loaded; map unavailable.');
			return;
		}
		setTimeout(() => {
			const mapEl = document.getElementById('map');
			if (!mapEl || mapEl._leaflet_id) return;

			mapRef.current = L.map('map', { zoomControl: true }).setView([15, 10], 2);

			L.tileLayer('https://{s}.basemaps.cartocdn.com/dark_all/{z}/{x}/{y}{r}.png', {
				maxZoom: 19,
				attribution: '&copy; OpenStreetMap contributors &copy; CARTO',
			}).addTo(mapRef.current);

			portLayerRef.current = L.layerGroup().addTo(mapRef.current);
			chokepointLayerRef.current = L.layerGroup().addTo(mapRef.current);

			setTimeout(() => {
				if (mapRef.current) {
					mapRef.current.invalidateSize();
				}
			}, 300);

			function handleResize() {
				if (mapRef.current) {
					mapRef.current.invalidateSize();
				}
			}
			window.addEventListener('resize', handleResize);

			if (mapEl.parentElement && typeof ResizeObserver !== 'undefined') {
				const observer = new ResizeObserver(() => {
					if (mapRef.current) mapRef.current.invalidateSize();
				});
				observer.observe(mapEl.parentElement);
				mapEl._resizeObserver = observer;
			}

			loadData();
		}, 50);

		return () => {
			window.removeEventListener('resize', () => {});
			if (mapRef.current) {
				mapRef.current.remove();
				mapRef.current = null;
			}
		};
	}, []);

	React.useEffect(() => {
		if (!mapRef.current) return;
		renderLayers();
	}, [ports, chokepoints, selectedItem]);

	const selectedData = selectedItem ? selectedItem.data : null;
	const selectedDate = selectedData ? selectedData.observed_on : null;

	const metrics = (() => {
		if (!selectedData) return [];
		const sv = selectedData.source_value || selectedData.metrics || {};
		const totalVessels = Number(sv.vessel_count_total);
		const items = [];
		if (sv.vessel_count_total !== undefined) items.push({ key: 'Total Vessels', value: formatCount(sv.vessel_count_total) });
		if (sv.vessel_count_container !== undefined) items.push({ key: 'Container Vessels', value: formatCount(sv.vessel_count_container) });
		if (sv.vessel_count_dry_bulk !== undefined) items.push({ key: 'Dry Bulk Vessels', value: formatCount(sv.vessel_count_dry_bulk) });
		if (sv.vessel_count_general_cargo !== undefined) items.push({ key: 'General Cargo Vessels', value: formatCount(sv.vessel_count_general_cargo) });
		if (sv.vessel_count_RoRo !== undefined) items.push({ key: 'RoRo Vessels', value: formatCount(sv.vessel_count_RoRo) });
		if (sv.vessel_count_tanker !== undefined) items.push({ key: 'Tanker Vessels', value: formatCount(sv.vessel_count_tanker) });
		if (Number.isFinite(totalVessels) && totalVessels > 0) {
			if (sv.vessel_count_container !== undefined) items.push({ key: 'Container Share', value: formatPercent((Number(sv.vessel_count_container) / totalVessels) * 100) });
			if (sv.vessel_count_dry_bulk !== undefined) items.push({ key: 'Dry Bulk Share', value: formatPercent((Number(sv.vessel_count_dry_bulk) / totalVessels) * 100) });
			if (sv.vessel_count_general_cargo !== undefined) items.push({ key: 'General Cargo Share', value: formatPercent((Number(sv.vessel_count_general_cargo) / totalVessels) * 100) });
			if (sv.vessel_count_RoRo !== undefined) items.push({ key: 'RoRo Share', value: formatPercent((Number(sv.vessel_count_RoRo) / totalVessels) * 100) });
			if (sv.vessel_count_tanker !== undefined) items.push({ key: 'Tanker Share', value: formatPercent((Number(sv.vessel_count_tanker) / totalVessels) * 100) });
		}
		if (sv.industry_top1) items.push({ key: 'Top Industry', value: sv.industry_top1 });
		if (sv.industry_top2) items.push({ key: 'Top Industry 2', value: sv.industry_top2 });
		if (sv.industry_top3) items.push({ key: 'Top Industry 3', value: sv.industry_top3 });
		if (sv.n_total !== undefined) items.push({ key: 'Total Transits', value: formatCount(sv.n_total) });
		if (sv.n_tanker !== undefined) items.push({ key: 'Tanker Transits', value: formatCount(sv.n_tanker) });
		if (sv.n_container !== undefined) items.push({ key: 'Container Transits', value: formatCount(sv.n_container) });
		if (sv.n_bulk !== undefined) items.push({ key: 'Bulk Carrier Transits', value: formatCount(sv.n_bulk) });
		return items;
	})();

	return e('div', { className: 'shell' },
		e('div', { className: 'topbar' },
			e('div', { className: 'topbar-meta' },
				e('div', { className: 'status-pill' },
					e('span', { className: 'status-dot' }),
					e('span', null, status)
				)
			)
		),
		e('div', { className: 'workspace' },
			e('div', { className: 'sidebar left-rail' },
				e('div', { className: 'panel section-surface' },
					e('div', { className: 'panel-body horizontal', style: { display: 'flex', alignItems: 'center', gap: '12px' } },
						e('div', { style: { flex: '1 1 auto' } },
							e('div', { className: 'panel-title' }, 'Selected record'),
							e('div', { className: 'summary-name' }, selectedData ? selectedData.name : 'Choose a record')
						),
						e('span', { className: 'chip' }, selectedDate ? formatDate(selectedDate) : 'Waiting for selection')
					)
				),
				e('section', { className: 'panel section-surface' },
					e('div', { className: 'panel-header' },
						e('div', { className: 'panel-title' }, '1. Port Metrics')
					),
					e('div', { className: 'panel-body' },
						metrics.length > 0
							? e('div', { className: 'metric-list' }, metrics.map(item => e('div', { key: item.key, className: 'metric-item' }, e('span', null, item.key), e('small', null, String(item.value)))))
							: e('div', { className: 'empty-state' }, 'No record selected.')
					)
				)
			),
			e('div', { className: 'map-shell' },
				e('div', { className: 'map-header' },
					e('div', { className: 'map-card' },
						e('span', { className: 'eyebrow' }, 'Map view'),
						e('h3', null, 'Global port activity'),
						e('p', null, 'Click a point to load details.')
					)
				),
				e('div', { id: 'map' })
			)
		)
	);
}

	ReactDOM.createRoot(document.getElementById('app')).render(e(App));
})();
