// Local wall-clock input, matching the recording timeline's Date/timezone model.
// Reject overflow (including nonexistent DST times) rather than silently seek elsewhere.
function parsePlayerDateTime(day, clock){
  if(!/^\d{4}-\d{2}-\d{2}$/.test(day)||!/^\d{2}:\d{2}(:\d{2})?$/.test(clock)) return null;
  var d=day.split('-').map(Number),t=clock.split(':').map(Number),sec=t[2]||0;
  var value=new Date(d[0],d[1]-1,d[2],t[0],t[1],sec);
  if(value.getFullYear()!==d[0]||value.getMonth()!==d[1]-1||value.getDate()!==d[2]||value.getHours()!==t[0]||value.getMinutes()!==t[1]||value.getSeconds()!==sec) return null;
  return value.getTime()/1000;
}
if(typeof module!=='undefined'&&module.exports) module.exports={parsePlayerDateTime};
