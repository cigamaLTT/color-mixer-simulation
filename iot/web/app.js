/* Dashboard state from backend telemetry and command events. */
(() => {
  "use strict";
  const $ = id => document.getElementById(id);
  const STATES = {idle:"Sẵn sàng",stirring:"Khuấy nguyên liệu",taring:"Đang trừ bì",dosing:"Đang định lượng",settling:"Ổn định cân",mixing:"Đang pha trộn",complete:"Hoàn tất",stopped:"Đã dừng",fault:"Có lỗi"};
  const COMMANDS = {start:"bắt đầu pha",stop:"dừng máy",tare:"trừ bì cân",reset:"đặt lại"};
  const ALLOWED = {start:new Set(["idle","complete","stopped"]),tare:new Set(["idle","complete","stopped"]),reset:new Set(["idle","complete","stopped","fault"])};
  const COLORS = ["#9eb88c","#e3bc97","#94bacd"];
  const INGREDIENTS = ["Xanh lá · CHINJOO 066","Màu da · CHINJOO 046","Xanh dương · CHINJOO 035"];
  const letters = ["A","B","C"];
  const devices = new Map(), histories = new Map(), pending = new Map(), confirmed = new Set(), seenEvents = new Set();
  let selected = "mixer-01", broker = {}, selectedComponent = "tank-0";
  let socket = null, socketConnected = false, reconnectTimer = null, reconnectDelay = 800, reconnectGeneration = 0;
  let lastHTTP = 0, fetching = false, staleAfter = 5000, offlineAfter = 15000;
  let logCount = 0, toastTimer = null, lastRenderKey = "";

  const finite = n => typeof n === "number" && Number.isFinite(n);
  const clamp = (n,min,max) => Math.max(min,Math.min(max,n));
  const number = (n,decimals=1) => finite(n) ? n.toLocaleString("vi-VN",{maximumFractionDigits:decimals}) : "—";
  const time = ms => new Date(ms).toLocaleTimeString("vi-VN",{hour:"2-digit",minute:"2-digit",second:"2-digit"});
  const ageText = ms => ms<1000 ? "Vừa xong" : ms<60000 ? `${Math.floor(ms/1000)} giây trước` : ms<3600000 ? `${Math.floor(ms/60000)} phút trước` : `${Math.floor(ms/3600000)} giờ trước`;
  const stateName = state => STATES[state] || (state ? `Trạng thái: ${String(state).slice(0,30)}` : "Chờ thiết bị");
  const vector = (value,length) => Array.isArray(value) && value.length>=length ? value : Array(length).fill(null);
  const asTimestamp = value => {const parsed = typeof value==="number" ? value : Date.parse(value);return Number.isFinite(parsed) ? parsed : null;};
  const getDevice = () => devices.get(selected);
  const isServerReachable = () => socketConnected || Date.now()-lastHTTP<6500;
  const isNative = data => data?.simulated===true || data?.session_id?.startsWith("native-");
  const remember = (set,key) => {set.add(key);if(set.size>128)set.delete(set.values().next().value);};

  function liveStatus(device=getDevice()) {
    const hasData = !!device?.telemetry;
    const seen = asTimestamp(device?.last_seen) ?? device?.received_at ?? null;
    const age = seen===null ? Infinity : Math.max(0,Date.now()-seen);
    const online = hasData && !!device?.online && age<=offlineAfter;
    const stale = !online || !!device?.stale || age>staleAfter;
    return {hasData,seen,age,online,stale,fresh:online&&!stale&&isServerReachable()&&broker.connected===true};
  }

  function addLog(title,detail="",severity="info") {
    const list = $("event-log"), item = document.createElement("li");
    if(severity!=="info") item.className=severity;
    const heading = document.createElement("div");heading.className="event-title";
    const label = document.createElement("span");label.textContent=title;
    const stamp = document.createElement("time");stamp.dateTime=new Date().toISOString();stamp.textContent=time(Date.now());
    heading.append(label,stamp);item.append(heading);
    if(detail){const body=document.createElement("div");body.className="event-detail";body.textContent=detail;item.append(body);}
    list.prepend(item);while(list.children.length>60)list.lastElementChild.remove();
    logCount++;$("log-count").textContent=`${logCount} sự kiện`;$("log-empty").hidden=true;
  }

  function toast(message,error=false) {
    clearTimeout(toastTimer);$("toast-text").textContent=message;$("toast").classList.toggle("error",error);$("toast").hidden=false;
    toastTimer=setTimeout(()=>{$("toast").hidden=true;},error?10000:7000);
  }

  function updateDevice(data,{telemetryMessage=false}={}) {
    if(!data || typeof data!=="object" || typeof data.device_id!=="string")return;
    const id=data.device_id, previous=devices.get(id);
    const device={...previous,...data};
    if(telemetryMessage){device.online=true;device.stale=false;device.last_seen=new Date().toISOString();device.received_at=Date.now();}
    if(device.telemetry && !previous?.telemetry)device.received_at=Date.now();
    devices.set(id,device);
    if(id===selected && previous && previous.online!==device.online)addLog(device.online?"Thiết bị đã kết nối":"Thiết bị ngắt kết nối",id,device.online?"info":"warning");
    const telemetry=device.telemetry;
    if(telemetry){
      recordTelemetry(id,telemetry);
      if(id===selected && previous?.telemetry?.state!==telemetry.state){
        let detail=id;
        if(telemetry.state==="dosing" && letters[telemetry.channel])detail+=` · Kênh ${letters[telemetry.channel]}`;
        if(telemetry.fault)detail+=` · ${String(telemetry.fault)}`;
        addLog(stateName(telemetry.state),detail,telemetry.state==="fault"?"error":"info");
      }
    }
    if(data.last_event)deviceEvent(id,data.last_event,{snapshot:true});
  }

  function recordTelemetry(id,data) {
    let history=histories.get(id);
    const session=data.session_id ?? "unknown";
    if(!history || history.session!==session){
      if(history && id===selected)addLog("Thiết bị bắt đầu phiên mới",id);
      history={session,seq:null,points:[],samples:0};histories.set(id,history);
    }
    if(data.seq!==undefined && data.seq===history.seq)return;
    history.seq=data.seq;history.samples++;
    if(data.scale_valid===true && finite(data.weight_g)){
      history.points.push({t:finite(data.uptime_ms)?data.uptime_ms:Date.now(),weight:data.weight_g,target:finite(data.target_g)?data.target_g:null});
      if(history.points.length>120)history.points.shift();
    }
  }

  function applySnapshot(data) {
    if(!data || typeof data!=="object")return;
    if(data.broker && typeof data.broker==="object"){
      const previous=broker.connected;broker=data.broker;
      if(previous!==broker.connected)addLog(broker.connected?"Máy chủ đã kết nối MQTT":"Kết nối MQTT gián đoạn",broker.mode==="embedded"?"Bộ chuyển tiếp MQTT cục bộ":broker.external_url || "Bộ chuyển tiếp MQTT",broker.connected?"info":"warning");
    }
    if(finite(data.stale_after_ms))staleAfter=data.stale_after_ms;
    if(finite(data.offline_after_ms))offlineAfter=data.offline_after_ms;
    if(Array.isArray(data.devices))data.devices.forEach(device=>updateDevice(device));
    if(!devices.has(selected) && devices.size)selected=devices.keys().next().value;
    renderDeviceSelect();render();
  }

  function receive(message) {
    if(!message || typeof message!=="object")return;
    if(message.type==="state"){applySnapshot(message.data);return;}
    const id=message.device_id;
    if(message.type==="telemetry" && message.data && typeof id==="string"){
      updateDevice({device_id:id,telemetry:message.data},{telemetryMessage:true});renderDeviceSelect();render();return;
    }
    if(message.type==="status"){
      updateDevice({device_id:id,...message.data});render();return;
    }
    if(message.type==="command_ack"){published(message.data,id);return;}
    if(message.type==="error"){
      const error=message.data || {};finishCommand(error.id);
      addLog("Không gửi được lệnh",error.message || error.code || "Lỗi từ máy chủ","error");
      toast(error.message || "Không gửi được lệnh điều khiển.",true);render();return;
    }
    if(message.type==="event"){deviceEvent(id,message.data);render();}
  }

  function deviceEvent(id,event,{snapshot=false}={}) {
    if(!event || typeof event!=="object")return;
    const key=`${id}:${JSON.stringify(event)}`;
    if(seenEvents.has(key))return;
    remember(seenEvents,key);
    const request=pending.get(event.id);
    if(event.type==="command_ack"){
      if(request && request.device_id!==id)return;
      remember(confirmed,`${id}:${event.id}`);
      finishCommand(event.id);
      // A snapshot may contain an event from before this page was opened.
      if(snapshot && !request)return;
      const accepted=event.accepted===true;
      const action=request ? `lệnh ${COMMANDS[request.type]}` : "lệnh điều khiển";
      const description=accepted?`Thiết bị đã chấp nhận ${action}.`:`Thiết bị từ chối ${action}${event.reason?`: ${event.reason}`:"."}`;
      addLog(accepted?"Thiết bị đã xác nhận":"Lệnh bị từ chối",`${id || selected} · ${description}`,accepted?"info":"error");
      if(id===selected || request?.device_id===selected)toast(description,!accepted);
    }else if(!snapshot && id===selected && event.type!=="state_changed"){
      addLog(event.message || event.reason || "Sự kiện từ thiết bị",event.type ? String(event.type) : id,event.type==="fault"?"error":"info");
    }
  }

  function renderDeviceSelect() {
    const selector=$("device-select"), ids=devices.size?[...devices.keys()]:[selected];
    if(selector.options.length===ids.length && ids.every((id,i)=>selector.options[i].value===id)){selector.value=selected;return;}
    selector.replaceChildren(...ids.map(id=>{const option=document.createElement("option");option.value=id;option.textContent=id;return option;}));selector.value=selected;
  }

  function setDot(id,status) {$(id).className=`status-dot${status?` ${status}`:""}`;}

  function renderConnection() {
    const status=liveStatus(), reachable=isServerReachable(), data=getDevice()?.telemetry;
    const transport=socketConnected?"WebSocket đang kết nối":reachable?"Kết nối HTTP dự phòng":"Mất kết nối máy chủ";
    const transportShort=socketConnected?"WebSocket trực tiếp":reachable?"Kết nối HTTP":"Chưa kết nối";
    $("transport-label").textContent=transportShort;setDot("transport-dot",socketConnected?"online":reachable?"warning":"");
    $("sidebar-status").textContent=socketConnected?"Máy chủ đã kết nối":reachable?"HTTP dự phòng":"Đang chờ kết nối";setDot("sidebar-dot",reachable?"online":"");
    $("log-status").textContent=socketConnected?"Nhận sự kiện trực tiếp":reachable?"Đang dùng kết nối HTTP":"Đang chờ kết nối";setDot("log-dot",reachable?"online":"");
    let title,detail,healthy=false,fault=false;
    if(!reachable){title="Chưa kết nối với máy chủ";detail="Hệ thống sẽ tự động kết nối lại. Các lệnh điều khiển đang tạm khóa.";}
    else if(broker.connected!==true){title="Chờ kết nối MQTT";detail="Máy chủ chưa kết nối bộ chuyển tiếp. Thiết bị chưa thể nhận lệnh điều khiển.";}
    else if(!status.hasData){title="Chờ thiết bị gửi dữ liệu";detail="Khởi động mô phỏng Wokwi hoặc thiết bị thật để bắt đầu quan sát và điều khiển.";}
    else if(!status.online){title="Thiết bị đang ngoại tuyến";detail="Đang hiển thị dữ liệu cuối cùng đã nhận. Kiểm tra kết nối của thiết bị.";}
    else if(status.stale){title="Dữ liệu thiết bị chưa được cập nhật";detail="Đang giữ số liệu gần nhất. Chỉ có lệnh dừng khả dụng khi dữ liệu đã cũ.";}
    else if(data?.fault || data?.state==="fault"){title="Thiết bị báo lỗi";detail=String(data.fault || "Kiểm tra thiết bị trước khi đặt lại.");fault=true;}
    else if(data?.scale_valid!==true){title="Chưa có tín hiệu cân hợp lệ";detail="Giá trị cân gần nhất chỉ để tham khảo. Kiểm tra cảm biến và trừ bì trước khi pha.";}
    else {title=ALLOWED.start.has(data.state)?"Thiết bị đã kết nối":"Đang vận hành mẻ pha";detail=`${selected}${isNative(data)?" · Bộ điều khiển Rust native với mô hình phần mềm":""} · ${socketConnected?"Dữ liệu được cập nhật trực tiếp qua WebSocket":"Đang cập nhật bằng HTTP trong lúc kết nối lại WebSocket"}.`;healthy=true;}
    $("notice-title").textContent=title;$("notice-detail").textContent=detail;$("connection-notice").classList.toggle("healthy",healthy);$("connection-notice").classList.toggle("fault",fault);
    if($("connection-dialog").open){
      const rows=[ ["Máy chủ",transport],["MQTT",broker.connected?"Đã kết nối":"Chưa kết nối"],["Chế độ MQTT",broker.mode==="embedded"?"Cục bộ":broker.mode==="external"?"Bên ngoài":"—"],["Địa chỉ MQTT",broker.external_url || broker.tcp_address || "—"],["Thiết bị",selected],["Nguồn dữ liệu",!data?"—":isNative(data)?"Rust native · mô hình phần mềm":"Thiết bị MQTT"],["Trạng thái dữ liệu",!status.hasData?"Chưa có dữ liệu":!status.online?"Ngoại tuyến":status.stale?"Dữ liệu cũ":"Trực tiếp"],["Bản tin gần nhất",data?.seq===undefined?"—":`#${data.seq}`],["Phiên thiết bị",data?.session_id || "—"],["Cập nhật gần nhất",status.seen?time(status.seen):"—"],["Ngưỡng dữ liệu cũ",`${number(staleAfter/1000)} giây`]];
      $("connection-details").replaceChildren(...rows.map(([label,value])=>{const row=document.createElement("div"),term=document.createElement("dt"),description=document.createElement("dd");term.textContent=label;description.textContent=String(value);row.append(term,description);return row;}));
    }
  }

  function render() {
    const device=getDevice(),data=device?.telemetry,status=liveStatus();
    renderConnection();
    $("machine-stage").classList.toggle("not-live",!status.fresh);
    $("overview").classList.toggle("is-stale-values",status.hasData&&!status.fresh);
    $("live-badge").classList.toggle("is-live",status.fresh);$("live-badge").classList.toggle("is-stale",status.hasData&&!status.fresh);
    $("live-label").textContent=status.fresh?(isNative(data)?"NATIVE · TRỰC TIẾP":"TRỰC TIẾP"):status.hasData?"DỮ LIỆU GẦN NHẤT":"CHƯA CÓ DỮ LIỆU";
    const validScale=data?.scale_valid===true;
    $("metric-weight").textContent=number(data?.weight_g);$("metric-target").textContent=number(data?.target_g);
    $("weight-note").textContent=!data?"Chưa nhận dữ liệu cân":!validScale?"Tín hiệu cân không hợp lệ":finite(data.gross_weight_g)?`Khối lượng cả bì: ${number(data.gross_weight_g)} g`:"Khối lượng sau khi trừ bì";
    $("target-note").textContent=data?.state==="dosing" && letters[data.channel]?`Đang cấp nguyên liệu ${letters[data.channel]}`:"Từ công thức trên thiết bị";
    const delivered=vector(data?.delivered_g,3),totalDelivered=delivered.every(finite)?delivered.reduce((a,b)=>a+b,0):null;
    $("metric-delivered").textContent=number(totalDelivered);
    const progress=finite(totalDelivered)&&finite(data?.target_g)&&data.target_g>0?clamp(totalDelivered/data.target_g*100,0,100):null;
    $("metric-progress").textContent=`${number(progress,0)} %`;$("batch-progress").style.width=`${progress??0}%`;
    $("metric-state").textContent=stateName(data?.state);
    $("device-state-note").textContent=!status.hasData?"Chưa có telemetry":!status.online?"Thiết bị ngoại tuyến":status.stale?"Dữ liệu đã cũ":!isServerReachable()?"Mất kết nối máy chủ":broker.connected!==true?"Mất kết nối MQTT":`Bản tin #${data.seq??"—"}`;
    setDot("device-dot",status.fresh?(data?.state==="fault"?"error":"online"):status.hasData?"warning":"");
    const levels=vector(data?.levels_pct,3),pumps=vector(data?.pumps,3),stirrers=vector(data?.stirrers,4);
    for(let i=0;i<3;i++){
      const level=finite(levels[i])?clamp(levels[i],0,100):0, height=level/100*145, y=263-height;
      $("tank-fill-"+i).setAttribute("y",String(y));$("tank-fill-"+i).setAttribute("height",String(height));$("tank-surface-"+i).setAttribute("cy",String(y));$("tank-surface-"+i).setAttribute("opacity",level>0?"1":"0");
      $("tank-level-"+i).replaceChildren(document.createTextNode(number(levels[i],0)),Object.assign(document.createElementNS("http://www.w3.org/2000/svg","tspan"),{textContent:" %"}));$("tank-level-"+i).lastChild.setAttribute("class","tank-percent");
      const running=finite(pumps[i])&&pumps[i]>0;
      $("pipe-"+i).classList.toggle("flowing",running&&status.fresh);$("pump-rotor-"+i).classList.toggle("running",running&&status.fresh);
      $("delivered-"+i).textContent=`${number(delivered[i])} g`;
    }
    for(let i=0;i<4;i++)$("stirrer-"+i).classList.toggle("running",stirrers[i]===true&&status.fresh);
    const cupPercent=finite(data?.weight_g)&&finite(data?.target_g)&&data.target_g>0?clamp(data.weight_g/data.target_g,0,1):0;
    $("cup-fill").setAttribute("y",String(435-cupPercent*75));$("cup-fill").setAttribute("height",String(cupPercent*75));$("cup-surface").setAttribute("cy",String(435-cupPercent*75));$("cup-surface").setAttribute("opacity",cupPercent>0?"1":"0");
    $("scale-readout").textContent=(!data?"—":!validScale?"ERR":number(data.weight_g))+" g";
    $("animation-label").textContent=status.fresh?"Chuyển động theo trạng thái thực":status.hasData?"Dữ liệu cũ · Chuyển động đã tạm dừng":"Chuyển động theo trạng thái thực";
    $("footer-freshness").textContent=status.seen?`${selected} · Cập nhật ${ageText(status.age).toLowerCase()}`:"Chưa nhận dữ liệu từ thiết bị";
    $("stat-updated").textContent=status.seen?ageText(status.age):"—";
    const history=histories.get(selected);$("stat-samples").replaceChildren(document.createTextNode(history?number(history.samples,0):"—"),Object.assign(document.createElement("small"),{textContent:" mẫu"}));
    const error=validScale&&finite(data?.weight_g)&&finite(data?.target_g)&&data.target_g>0?data.weight_g-data.target_g:null;
    $("stat-error").replaceChildren(document.createTextNode(finite(error)?`${error>0?"+":""}${number(error)}`:"—"),Object.assign(document.createElement("small"),{textContent:" g"}));
    renderInspector();renderControls();
    const chartKey=`${selected}:${history?.session}:${history?.samples}:${data?.target_g}`;
    if(chartKey!==lastRenderKey){lastRenderKey=chartKey;renderChart();}
  }

  function renderInspector() {
    const data=getDevice()?.telemetry,status=liveStatus(),[kind,index]=selectedComponent.split("-"),i=Number(index);
    const details=[];
    if(kind==="tank"){
      $("component-title").textContent=`Bồn ${letters[i]}`;$("component-subtitle").textContent=INGREDIENTS[i];$("component-swatch").style.background=COLORS[i];
      if(data){details.push(["Mức bồn",`${number(data.levels_pct?.[i],0)}%`],["Bơm",finite(data.pumps?.[i])?(data.pumps[i]>0?"Đang chạy":"Đã tắt"):"—"],["Khuấy",typeof data.stirrers?.[i]==="boolean"?(data.stirrers[i]?"Đang chạy":"Đã tắt"):"—"]);}
    }else if(kind==="pump"){
      $("component-title").textContent=`Bơm P${i+1}`;$("component-subtitle").textContent=`Định lượng nguyên liệu ${letters[i]}`;$("component-swatch").style.background=COLORS[i];
      if(data)details.push(["Trạng thái",finite(data.pumps?.[i])?(data.pumps[i]>0?"Đang chạy":"Đã tắt"):"—"],["Công suất",finite(data.pumps?.[i])?`${number(clamp(data.pumps[i],0,1)*100,0)}%`:"—"],["Đã cấp",`${number(data.delivered_g?.[i])} g`]);
    }else{
      $("component-title").textContent="Cốc & cân";$("component-subtitle").textContent="Cân thành phẩm · Bộ khuấy 4";$("component-swatch").style.background="#b6bd9a";
      if(data)details.push(["Khối lượng",`${number(data.weight_g)} g`],["Cảm biến",data.scale_valid?"Hợp lệ":"Lỗi cân"],["Khuấy",data.stirrers?.[3]?"Đang chạy":"Đã tắt"]);
    }
    $("component-details").replaceChildren();
    if(!data){$("component-details").textContent="Chưa có dữ liệu từ thiết bị";return;}
    for(const [label,value] of details){const span=document.createElement("span"),strong=document.createElement("strong");span.textContent=label;strong.textContent=value;span.append(strong);$("component-details").append(span);}
    if(!status.fresh)$("component-details").title="Trạng thái trong bản tin gần nhất; chưa có dữ liệu trực tiếp.";
    else $("component-details").removeAttribute("title");
  }

  function recipe() {
    const total=$("total-input").valueAsNumber,ratios=[0,1,2].map(i=>$("ratio-"+i).valueAsNumber),sum=ratios.reduce((a,b)=>a+b,0);
    let error="";
    if(!finite(total)||total<1||total>875)error="Tổng khối lượng phải từ 1 đến 875 g.";
    else if(ratios.some(n=>!finite(n)||n<0||n>100))error="Mỗi tỷ lệ phải nằm trong khoảng 0–100%.";
    else if(Math.abs(sum-100)>0.000001)error="Tổng tỷ lệ của ba nguyên liệu phải bằng 100%.";
    return {total,ratios,sum,error,valid:!error};
  }

  function updateRecipe() {
    const current=recipe(),validRatios=current.ratios.every(n=>finite(n)&&n>=0&&n<=100)&&Math.abs(current.sum-100)<=0.000001;
    for(let i=0;i<3;i++){
      $("planned-"+i).textContent=`${number(finite(current.total)&&finite(current.ratios[i])?current.total*current.ratios[i]/100:null,2)} g`;
      $("ratio-bar-"+i).style.width=`${finite(current.ratios[i])?clamp(current.ratios[i],0,100):0}%`;
      const invalid=!finite(current.ratios[i])||current.ratios[i]<0||current.ratios[i]>100;
      $("ratio-"+i).classList.toggle("invalid-input",invalid);$("ratio-"+i).setAttribute("aria-invalid",String(invalid));
    }
    const invalidTotal=!finite(current.total)||current.total<1||current.total>875;
    $("total-input").classList.toggle("invalid-input",invalidTotal);$("total-input").setAttribute("aria-invalid",String(invalidTotal));
    $("ratio-total").textContent=`${number(current.sum,2)}%`;$("ratio-summary").classList.toggle("invalid",!validRatios);$("ratio-summary").lastElementChild.textContent=validRatios?"Hợp lệ":"Cần điều chỉnh";
    $("recipe-error").hidden=current.valid;$("recipe-error").textContent=current.error;
    renderControls();
  }

  function commandBlock(type) {
    const device=getDevice(),data=device?.telemetry,status=liveStatus();
    if(!isServerReachable())return "Chờ kết nối máy chủ để điều khiển thiết bị.";
    if(broker.connected!==true)return "Chờ máy chủ kết nối MQTT để gửi lệnh.";
    if(!status.hasData || !status.online)return "Kết nối thiết bị để gửi lệnh điều khiển.";
    const inFlight=[...pending.values()].some(p=>p.device_id===selected && (type!=="stop" || p.type==="stop"));
    if(inFlight)return "Đã gửi lệnh, đang chờ thiết bị xác nhận.";
    if(type==="stop")return "";
    if(status.stale)return "Dữ liệu đã cũ. Chờ cập nhật; vẫn có thể dừng máy.";
    if(!ALLOWED[type]?.has(data.state))return "Chờ thiết bị hoàn thành hoặc dừng mẻ hiện tại.";
    if(type==="start"){
      const current=recipe();if(!current.valid)return current.error;
      if(data.scale_valid!==true)return "Cân chưa có tín hiệu hợp lệ để bắt đầu pha.";
      if(finite(data.gross_weight_g)&&data.gross_weight_g+current.total>995)return "Tổng tải trọng sau pha vượt giới hạn cân 995 g.";
      if(data.fault)return "Kiểm tra và đặt lại lỗi thiết bị trước khi pha.";
    }
    return "";
  }

  function renderControls() {
    for(const type of ["start","stop","tare","reset"]){const reason=commandBlock(type),button=$(type+"-button");button.disabled=!!reason;button.title=reason || `${COMMANDS[type][0].toUpperCase()+COMMANDS[type].slice(1)} trên ${selected}`;}
    $("control-help").textContent=commandBlock("start") || "Lệnh được gửi đến thiết bị. Trạng thái chỉ đổi khi nhận dữ liệu.";
  }

  function finishCommand(id) {const request=pending.get(id);if(request){clearTimeout(request.timer);pending.delete(id);}}

  function published(data,id) {
    if(!data || data.status!=="published")return;
    const request=pending.get(data.id);
    if(request?.published)return;
    if(request)request.published=true;
    const hasConfirmation=confirmed.has(`${id || data.device_id}:${data.id}`);
    addLog("Đã chuyển lệnh đến MQTT",`${id || data.device_id || selected} · ${hasConfirmation?"Thiết bị đã phản hồi lệnh":"Chờ thiết bị xác nhận"}`);
    // The firmware event can arrive before the backend finishes publishing.
    if(!hasConfirmation && (request?.device_id || id || data.device_id)===selected)toast("Đã gửi lệnh. Đang chờ thiết bị xác nhận.");
  }

  async function sendCommand(type) {
    const blocked=commandBlock(type);if(blocked){toast(blocked,true);return;}
    const id=globalThis.crypto?.randomUUID?.() || `web-${Date.now()}-${Math.random().toString(36).slice(2,10)}`;
    const command={id,type};if(type!=="stop")command.session_id=getDevice().telemetry.session_id;
    if(type==="start"){const current=recipe();command.total_g=current.total;command.ratios=current.ratios;}
    const envelope={type:"command",device_id:selected,command};
    const timer=setTimeout(()=>{pending.delete(id);addLog("Chưa nhận xác nhận từ thiết bị",`Lệnh ${COMMANDS[type]} · Kiểm tra trạng thái thực trước khi gửi lại`,"warning");toast("Chưa nhận được xác nhận. Kiểm tra trạng thái thiết bị trước khi gửi lại.",true);renderControls();},10000);
    pending.set(id,{device_id:selected,type,timer,published:false});renderControls();addLog(`Yêu cầu ${COMMANDS[type]}`,selected);
    try{
      if(socketConnected && socket?.readyState===WebSocket.OPEN)socket.send(JSON.stringify(envelope));
      else{
        const response=await fetch("/api/command",{method:"POST",headers:{"Content-Type":"application/json"},body:JSON.stringify(envelope),signal:AbortSignal.timeout(7000)});
        const result=await response.json();
        if(!response.ok)throw new Error(result.error?.message || result.error || `Lỗi HTTP ${response.status}`);
        published(result,envelope.device_id);
      }
    }catch(error){finishCommand(id);addLog("Không gửi được lệnh",String(error.message),"error");toast(`Không gửi được lệnh: ${error.message}`,true);renderControls();}
  }

  function renderChart() {
    const points=histories.get(selected)?.points || [],grid=$("chart-grid"),labels=$("chart-labels");
    grid.replaceChildren();labels.replaceChildren();
    const ns="http://www.w3.org/2000/svg",make=(tag,attrs,text)=>{const el=document.createElementNS(ns,tag);for(const [key,val] of Object.entries(attrs))el.setAttribute(key,String(val));if(text!==undefined)el.textContent=text;return el;};
    const left=45,right=681,top=21,bottom=177;
    const maxValue=Math.max(10,...points.map(p=>p.weight),...points.map(p=>p.target || 0));
    const minValue=Math.min(0,...points.map(p=>p.weight));
    const yMax=Math.ceil(maxValue/10)*10,yMin=Math.floor(minValue/10)*10;
    for(let i=0;i<4;i++){
      const y=top+(bottom-top)*i/3;grid.append(make("line",{x1:left,y1:y,x2:right,y2:y,stroke:"#eef1e7","stroke-dasharray":"3 4"}));
      if(points.length)labels.append(make("text",{x:left-10,y:y+3,fill:"#adb79b","font-size":8,"text-anchor":"end"},number(yMax-(yMax-yMin)*i/3,0)));
    }
    $("chart-empty").hidden=points.length>0;
    if(!points.length){$("chart-line").setAttribute("d","");$("chart-area").setAttribute("d","");$("chart-target").setAttribute("d","");$("chart-point").setAttribute("visibility","hidden");$("weight-chart").setAttribute("aria-label","Biểu đồ khối lượng theo thời gian, chưa có dữ liệu");return;}
    const first=points[0].t,last=points.at(-1).t,span=Math.max(1000,last-first);
    const x=t=>left+clamp((t-first)/span,0,1)*(right-left),y=v=>bottom-(v-yMin)/(yMax-yMin)*(bottom-top);
    const path=points.map((point,i)=>`${i?"L":"M"}${x(point.t).toFixed(2)},${y(point.weight).toFixed(2)}`).join(" ");
    $("chart-line").setAttribute("d",path);$("chart-area").setAttribute("d",`${path} L${x(last)},${bottom} L${left},${bottom} Z`);
    const target=getDevice()?.telemetry?.target_g;$("chart-target").setAttribute("d",finite(target)&&target>0?`M${left},${y(target)} H${right}`:"");
    $("chart-point").setAttribute("cx",x(last));$("chart-point").setAttribute("cy",y(points.at(-1).weight));$("chart-point").setAttribute("visibility","visible");
    for(let i=0;i<5;i++){const seconds=(span/1000*i/4),label=seconds>=60?`${Math.floor(seconds/60)}:${String(Math.round(seconds%60)).padStart(2,"0")}`:`${number(seconds,0)} s`;labels.append(make("text",{x:left+(right-left)*i/4,y:199,fill:"#a9b498","font-size":8,"text-anchor":i===0?"start":i===4?"end":"middle"},label));}
    labels.append(make("text",{x:16,y:11,fill:"#a7b397","font-size":7},"g"));
    $("weight-chart").setAttribute("aria-label",`Biểu đồ ${points.length} mẫu cân gần nhất. Khối lượng ${number(points.at(-1).weight)} gram; mục tiêu ${number(target)} gram.`);
  }

  async function fetchState() {
    if(fetching || socketConnected)return;
    fetching=true;
    try{const response=await fetch("/api/state",{cache:"no-store",signal:AbortSignal.timeout(4000)});if(!response.ok)throw new Error(`HTTP ${response.status}`);const data=await response.json();lastHTTP=Date.now();applySnapshot(data?.type==="state"?data.data:data);}
    catch{renderConnection();renderControls();}
    finally{fetching=false;}
  }

  function connect() {
    clearTimeout(reconnectTimer);const generation=++reconnectGeneration;
    if(socket){socket.onclose=null;socket.close();}socketConnected=false;
    try{socket=new WebSocket(`${location.protocol==="https:"?"wss:":"ws:"}//${location.host}/ws`);}
    catch{scheduleReconnect(generation);fetchState();return;}
    socket.onopen=()=>{if(generation!==reconnectGeneration)return;socketConnected=true;reconnectDelay=800;addLog("Đã kết nối máy chủ","Đang nhận dữ liệu và sự kiện trực tiếp");render();};
    socket.onmessage=event=>{if(generation!==reconnectGeneration)return;try{receive(JSON.parse(event.data));}catch{/* A malformed frame must not stop subsequent telemetry. */}};
    socket.onclose=()=>{if(generation!==reconnectGeneration)return;const wasConnected=socketConnected;socketConnected=false;if(wasConnected)addLog("Kết nối trực tiếp bị gián đoạn","Đang thử kết nối lại và cập nhật qua HTTP","warning");render();scheduleReconnect(generation);fetchState();};
    socket.onerror=()=>{/* onclose owns reconnection and HTTP fallback. */};
  }

  function scheduleReconnect(generation) {if(generation!==reconnectGeneration)return;clearTimeout(reconnectTimer);reconnectTimer=setTimeout(connect,reconnectDelay);reconnectDelay=Math.min(reconnectDelay*1.7,10000);}

  $("recipe-form").addEventListener("submit",event=>{event.preventDefault();sendCommand("start");});
  ["total-input","ratio-0","ratio-1","ratio-2"].forEach(id=>$(id).addEventListener("input",updateRecipe));
  $("equal-ratios").addEventListener("click",()=>{[33.34,33.33,33.33].forEach((value,i)=>$("ratio-"+i).value=value);updateRecipe();});
  ["stop","tare","reset"].forEach(type=>$(type+"-button").addEventListener("click",()=>sendCommand(type)));
  $("device-select").addEventListener("change",event=>{selected=event.target.value;lastRenderKey="";render();});
  document.querySelectorAll("[data-component]").forEach(component=>{
    const select=()=>{selectedComponent=component.dataset.component;document.querySelectorAll("[data-component]").forEach(other=>{const active=other===component;other.classList.toggle("selected",active);other.setAttribute("aria-pressed",String(active));});renderInspector();};
    component.addEventListener("click",select);component.addEventListener("keydown",event=>{if(event.key==="Enter"||event.key===" "){event.preventDefault();select();}});
  });
  document.querySelectorAll("[data-open-connection]").forEach(button=>button.addEventListener("click",()=>{$("connection-dialog").showModal();renderConnection();}));
  $("close-connection").addEventListener("click",()=>$("connection-dialog").close());
  $("connection-dialog").addEventListener("click",event=>{if(event.target===$("connection-dialog")){const box=event.target.getBoundingClientRect();if(event.clientX<box.left||event.clientX>box.right||event.clientY<box.top||event.clientY>box.bottom)event.target.close();}});
  $("retry-connection").addEventListener("click",()=>{connect();fetchState();toast("Đang kết nối lại với máy chủ.");});
  $("close-toast").addEventListener("click",()=>{$("toast").hidden=true;clearTimeout(toastTimer);});
  $("clear-log").addEventListener("click",()=>{$("event-log").replaceChildren();logCount=0;$("log-count").textContent="0 sự kiện";$("log-empty").hidden=false;});
  $("local-date").textContent=new Intl.DateTimeFormat("vi-VN",{day:"2-digit",month:"short",year:"numeric"}).format(new Date());
  document.addEventListener("visibilitychange",()=>{if(!document.hidden){render();if(!socketConnected)fetchState();}});
  setInterval(()=>{render();if(!socketConnected)fetchState();},1500);
  window.addEventListener("online",()=>{if(!socketConnected){connect();fetchState();}});
  updateRecipe();render();connect();fetchState();
})();
