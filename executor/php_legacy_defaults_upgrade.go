package executor

import (
	"os"

	"github.com/naibabiji/wp-panel/database"
)

func init() {
	database.RegisterUpgrade("1.0.51", UpgradeLegacyPHPMaxInputVars)
}

// UpgradeLegacyPHPMaxInputVars 把仍停留在旧默认值 2000 的 max_input_vars 升级为新默认值 10000。
//
// 只有当前值精确等于旧默认值时才会覆盖；管理员已手动改过的值（包括手动设成别的数字，
// 哪怕碰巧也是 2000）不受影响，因为配置文件本身不区分"未修改的默认值"和"用户手动设置的值"，
// 这是当前存储模型下能做到的最安全近似。
//
// 这是一次性版本迁移（版本号 1.0.51），不会在每次面板启动时重复检查——用户升级后如果
// 自己又把值改回 2000，是他的选择，不会被再次覆盖。
func UpgradeLegacyPHPMaxInputVars() error {
	data, err := os.ReadFile(phpRuntimeConfigPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // 尚未初始化；EnsurePHPRuntimeConfigFile 会直接用新默认值写入
		}
		return err
	}

	content := string(data)
	if findIniValue(content, "max_input_vars") != "2000" {
		return nil
	}

	next := setIniValue(content, "max_input_vars", "10000")
	return os.WriteFile(phpRuntimeConfigPath, []byte(next), 0644)
}
