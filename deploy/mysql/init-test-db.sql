-- 集成测试专用库。
--
-- 为什么单独建库而不是复用 medicore：
-- 集成测试每次运行都会 DROP 并重建库以保证用例之间互不影响，
-- 若直接跑在开发库上，一次测试就会把演示数据全部清掉。
--
-- 只在数据卷为空时执行（与 01-schema.sql 一样），因此对已有环境
-- 需要手动执行一次，或 `docker compose down -v` 后重建。
CREATE DATABASE IF NOT EXISTS medicore_test
  CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci;

-- 应用账号需要建库/删库权限，测试才能自行准备与清理。
-- 仅授权到 medicore_test，不影响其他库。
GRANT ALL PRIVILEGES ON medicore_test.* TO 'medicore'@'%';

FLUSH PRIVILEGES;
