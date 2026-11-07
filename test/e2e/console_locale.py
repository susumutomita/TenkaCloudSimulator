"""Switch the real global preference through the active provider's navigation.

AWS uses its Cloudscape settings gear and Language submenu. Other providers keep
the neutral native select. No forced clicks, DOM changes or authority changes.
"""
from playwright.sync_api import expect


def select_console_locale(page, value, audit=None, keyboard=False):
    assert value in ('en', 'ja'), 'Only implemented console locales are selectable'
    current = page.locator('html').get_attribute('lang')
    assert current in ('en', 'ja'), 'Expected an initialized console locale'
    providers = page.get_by_role('navigation', name='クラウドプロバイダー' if current == 'ja' else 'Cloud provider', exact=True)
    aws = providers.get_by_role('button', name='AWS', exact=True)
    if aws.get_attribute('aria-pressed') == 'true':
        navigation = page.locator('.aws-console-top-navigation')
        gear = navigation.get_by_role('button', name='設定' if current == 'ja' else 'Settings', exact=True)
        expect(gear).to_be_enabled()
        if keyboard:
            gear.focus()
            gear.press('Enter')
        else:
            gear.click()
        expect(gear).to_have_attribute('aria-expanded', 'true')
        if audit:
            audit('aws-settings-open')
        language = page.get_by_role('menuitem', name='言語' if current == 'ja' else 'Language', exact=True)
        if keyboard:
            expect(language).to_be_focused()
            language.press('Enter')
        else:
            language.click()
        expect(language).to_have_attribute('aria-expanded', 'true')
        if audit:
            audit('aws-language-open')
        choice = page.get_by_role('menuitemcheckbox', name='日本語' if value == 'ja' else 'English', exact=True)
        expect(choice).to_have_attribute('aria-checked', 'true' if value == current else 'false')
        choice.click()
        updated_gear = navigation.get_by_role('button', name='設定' if value == 'ja' else 'Settings', exact=True)
        expect(updated_gear).to_have_attribute('aria-expanded', 'false')
        if keyboard:
            expect(updated_gear).to_be_focused()
    else:
        page.get_by_role('combobox', name='言語' if current == 'ja' else 'Language', exact=True).select_option(value)
    expect(page.locator('html')).to_have_attribute('lang', value)
