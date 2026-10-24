// Fills in the GitHub links automatically from the page address
// (https://USERNAME.github.io/REPOSITORY/), so nothing needs editing.
(function () {
  var host = location.hostname, parts = location.pathname.split("/").filter(Boolean);
  var repo = null;
  if (/\.github\.io$/i.test(host)) {
    var user = host.split(".")[0];
    var name = parts.length && !/\.html?$/i.test(parts[0]) ? parts[0] : user + ".github.io";
    repo = "https://github.com/" + user + "/" + name;
  }
  document.querySelectorAll(".repo-link").forEach(function (a) {
    if (repo) a.href = repo; else a.style.display = "none";
  });
  document.querySelectorAll(".dl-link").forEach(function (a) {
    a.href = repo ? repo + "/releases/latest" : "#";
  });
})();
